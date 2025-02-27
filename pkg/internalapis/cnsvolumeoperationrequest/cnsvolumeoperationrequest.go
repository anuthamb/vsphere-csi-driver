/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cnsvolumeoperationrequest

import (
	"context"
	"fmt"
	"reflect"

	"github.com/davecgh/go-spew/spew"
	"github.com/pkg/errors"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	csiconfig "sigs.k8s.io/vsphere-csi-driver/pkg/common/config"
	"sigs.k8s.io/vsphere-csi-driver/pkg/csi/service/logger"
	cnsvolumeoperationrequestv1alpha1 "sigs.k8s.io/vsphere-csi-driver/pkg/internalapis/cnsvolumeoperationrequest/v1alpha1"
	k8s "sigs.k8s.io/vsphere-csi-driver/pkg/kubernetes"
)

// VolumeOperationRequest is an interface that supports handling idempotency
// in CSI volume manager. This interface persists operation details invoked
// on CNS and returns the persisted information to callers whenever it is requested.
type VolumeOperationRequest interface {
	// GetRequestDetails returns the details of the operation on the volume
	// that is persisted by the VolumeOperationRequest interface.
	// Returns an error if any error is encountered while attempting to
	// read the previously persisted information.
	GetRequestDetails(ctx context.Context, name string) (*VolumeOperationRequestDetails, error)
	// StoreRequestDetails persists the details of the operation taking
	// place on the volume.
	// Returns an error if any error is encountered. Clients must assume
	// that the attempt to persist the information failed if an error is returned.
	StoreRequestDetails(ctx context.Context, instance *VolumeOperationRequestDetails) error
}

// operationRequestStore implements the VolumeOperationsRequest interface.
// This implementation persists the operation information on etcd via a client
// to the API server. Reads are also done directly on etcd; there is no caching
// layer involved.
type operationRequestStore struct {
	k8sclient client.Client
}

// InitVolumeOperationRequestInterface creates the CnsVolumeOperationRequest
// definition on the API server and returns an implementation of
// VolumeOperationRequest interface. Clients are unaware of the implementation
// details to read and persist volume operation details.
// This function is not thread safe. Multiple serial calls to this function will
// return multiple new instances of the VolumeOperationRequest interface.
// TODO: Make this thread-safe and a singleton.
func InitVolumeOperationRequestInterface(ctx context.Context) (VolumeOperationRequest, error) {
	log := logger.GetLogger(ctx)
	// Create CnsVolumeOperationRequest definition on API server
	log.Info("Creating cnsvolumeoperationrequest definition on API server")
	err := k8s.CreateCustomResourceDefinitionFromSpec(ctx, crdName, crdSingular, crdPlural,
		reflect.TypeOf(cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequest{}).Name(), cnsvolumeoperationrequestv1alpha1.SchemeGroupVersion.Group, cnsvolumeoperationrequestv1alpha1.SchemeGroupVersion.Version, apiextensionsv1beta1.NamespaceScoped)
	if err != nil {
		log.Errorf("failed to create cnsvolumeoperationrequest CRD with error: %v", err)
	}

	// Get in cluster config for client to API server
	config, err := k8s.GetKubeConfig(ctx)
	if err != nil {
		log.Errorf("failed to get kubeconfig with error: %v", err)
		return nil, err
	}

	// Create client to API server
	k8sclient, err := k8s.NewClientForGroup(ctx, config, cnsvolumeoperationrequestv1alpha1.SchemeGroupVersion.Group)
	if err != nil {
		log.Errorf("failed to create k8sClient with error: %v", err)
		return nil, err
	}

	// Initialize the operationRequestStore implementation of VolumeOperationRequest
	// interface.
	// NOTE: Currently there is only a single implementation of this interface.
	// Future implementations will need modify this step.
	operationRequestStore := &operationRequestStore{
		k8sclient: k8sclient,
	}

	return operationRequestStore, nil
}

// GetRequestDetails returns the details of the operation on the volume
// that is persisted by the VolumeOperationRequest interface, by querying
// API server for a CnsVolumeOperationRequest instance with the given
// name.
// Returns an error if any error is encountered while attempting to
// read the previously persisted information from the API server.
// Callers need to differentiate NotFound errors if required.
func (or *operationRequestStore) GetRequestDetails(ctx context.Context, name string) (*VolumeOperationRequestDetails, error) {
	log := logger.GetLogger(ctx)
	instanceKey := client.ObjectKey{Name: name, Namespace: csiconfig.DefaultCSINamespace}
	log.Debugf("Getting CnsVolumeOperationRequest instance with name %s/%s", instanceKey.Namespace, instanceKey.Name)

	instance := &cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequest{}
	err := or.k8sclient.Get(ctx, instanceKey, instance)
	if err != nil {
		return nil, err
	}
	log.Debugf("Found CnsVolumeOperationRequest instance %v", spew.Sdump(instance))

	if len(instance.Status.LatestOperationDetails) == 0 {
		return nil, fmt.Errorf("length of LatestOperationDetails expected to be greater than 1 if the instance exists")
	}

	// Callers only need to know about the last operation that was invoked on a volume.
	operationDetailsToReturn := instance.Status.LatestOperationDetails[len(instance.Status.LatestOperationDetails)-1]

	return CreateVolumeOperationRequestDetails(instance.Spec.Name, instance.Status.VolumeID, instance.Status.SnapshotID,
			instance.Status.Capacity, operationDetailsToReturn.TaskInvocationTimestamp, operationDetailsToReturn.TaskID,
			operationDetailsToReturn.OpID, operationDetailsToReturn.TaskStatus, operationDetailsToReturn.Error),
		nil
}

// StoreRequestDetails persists the details of the operation taking
// place on the volume by storing it on the API server.
// Returns an error if any error is encountered. Clients must assume
// that the attempt to persist the information failed if an error is returned.
func (or *operationRequestStore) StoreRequestDetails(ctx context.Context, operationToStore *VolumeOperationRequestDetails) error {
	log := logger.GetLogger(ctx)
	if operationToStore == nil {
		msg := "cannot store empty operation"
		log.Error(msg)
		return errors.New(msg)
	}
	log.Debugf("Storing CnsVolumeOperationRequest instance with spec %v", spew.Sdump(operationToStore))

	operationDetailsToStore := convertToCnsVolumeOperationRequestDetails(*operationToStore.OperationDetails)
	instance := &cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequest{}
	instanceKey := client.ObjectKey{Name: operationToStore.Name, Namespace: csiconfig.DefaultCSINamespace}

	if err := or.k8sclient.Get(ctx, instanceKey, instance); err != nil {
		if apierrors.IsNotFound(err) {
			// Create new instance on API server if it doesnt exist. Implies that this is the first time this object is being stored.
			newInstance := &cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      instanceKey.Name,
					Namespace: instanceKey.Namespace,
				},
				Spec: cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequestSpec{
					Name: instanceKey.Name,
				},
				Status: cnsvolumeoperationrequestv1alpha1.CnsVolumeOperationRequestStatus{
					VolumeID:              operationToStore.VolumeID,
					SnapshotID:            operationToStore.SnapshotID,
					Capacity:              operationToStore.Capacity,
					FirstOperationDetails: *operationDetailsToStore,
					LatestOperationDetails: []cnsvolumeoperationrequestv1alpha1.OperationDetails{
						*operationDetailsToStore,
					},
				},
			}
			err = or.k8sclient.Create(ctx, newInstance)
			if err != nil {
				log.Errorf("failed to create CnsVolumeOperationRequest instance %s/%s with error: %v", instanceKey.Namespace, instanceKey.Name, err)
				return err
			}
			log.Debugf("Created CnsVolumeOperationRequest instance %s/%s with latest information for task with ID: %s", instanceKey.Namespace, instanceKey.Name, operationDetailsToStore.TaskID)
			return nil
		}
		log.Errorf("failed to get CnsVolumeOperationRequest instance %s/%s with error: %v", instanceKey.Namespace, instanceKey.Name, err)
		return err
	}

	// Create a deep copy since we modify the object.
	updatedInstance := instance.DeepCopy()

	// Modify VolumeID, SnapshotID and Capacity
	updatedInstance.Status.VolumeID = operationToStore.VolumeID
	updatedInstance.Status.SnapshotID = operationToStore.SnapshotID
	updatedInstance.Status.Capacity = operationToStore.Capacity

	// Modify FirstOperationDetails only if it doesnt exist or TaskID's match.
	firstOp := instance.Status.FirstOperationDetails
	if firstOp.TaskID == "" || firstOp.TaskID == operationToStore.OperationDetails.TaskID {
		updatedInstance.Status.FirstOperationDetails = *operationDetailsToStore
	}

	operationExistsInList := false
	// If the task details already exist in the status, update it with the latest information.
	for index := len(instance.Status.LatestOperationDetails) - 1; index >= 0; index-- {
		operationDetail := instance.Status.LatestOperationDetails[index]
		if operationDetailsToStore.TaskID == operationDetail.TaskID {
			updatedInstance.Status.LatestOperationDetails[index] = *operationDetailsToStore
			operationExistsInList = true
			break
		}
	}

	if !operationExistsInList {
		// Append the latest task details to the local instance and ensure length of LatestOperationDetails is not greater than 10.
		updatedInstance.Status.LatestOperationDetails = append(updatedInstance.Status.LatestOperationDetails, *operationDetailsToStore)
		if len(updatedInstance.Status.LatestOperationDetails) > maxEntriesInLatestOperationDetails {
			updatedInstance.Status.LatestOperationDetails = updatedInstance.Status.LatestOperationDetails[1:]
		}
	}

	// Store the local instance on the API server.
	err := or.k8sclient.Update(ctx, updatedInstance)
	if err != nil {
		log.Errorf("failed to update CnsVolumeOperationRequest instance %s/%s with error: %v", instanceKey.Namespace, instanceKey.Name, err)
		return err
	}
	log.Debugf("Updated CnsVolumeOperationRequest instance %s/%s with latest information for task with ID: %s", instanceKey.Namespace, instanceKey.Name, operationDetailsToStore.TaskID)
	return nil
}
