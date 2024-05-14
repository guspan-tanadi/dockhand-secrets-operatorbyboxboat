/*
Copyright © 2024 BoxBoat

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
// Code generated by main. DO NOT EDIT.

package v1alpha2

import (
	"context"
	"sync"
	"time"

	v1alpha2 "github.com/boxboat/dockhand-secrets-operator/pkg/apis/dhs.dockhand.dev/v1alpha2"
	"github.com/rancher/wrangler/v2/pkg/apply"
	"github.com/rancher/wrangler/v2/pkg/condition"
	"github.com/rancher/wrangler/v2/pkg/generic"
	"github.com/rancher/wrangler/v2/pkg/kv"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SecretController interface for managing Secret resources.
type SecretController interface {
	generic.ControllerInterface[*v1alpha2.Secret, *v1alpha2.SecretList]
}

// SecretClient interface for managing Secret resources in Kubernetes.
type SecretClient interface {
	generic.ClientInterface[*v1alpha2.Secret, *v1alpha2.SecretList]
}

// SecretCache interface for retrieving Secret resources in memory.
type SecretCache interface {
	generic.CacheInterface[*v1alpha2.Secret]
}

// SecretStatusHandler is executed for every added or modified Secret. Should return the new status to be updated
type SecretStatusHandler func(obj *v1alpha2.Secret, status v1alpha2.SecretStatus) (v1alpha2.SecretStatus, error)

// SecretGeneratingHandler is the top-level handler that is executed for every Secret event. It extends SecretStatusHandler by a returning a slice of child objects to be passed to apply.Apply
type SecretGeneratingHandler func(obj *v1alpha2.Secret, status v1alpha2.SecretStatus) ([]runtime.Object, v1alpha2.SecretStatus, error)

// RegisterSecretStatusHandler configures a SecretController to execute a SecretStatusHandler for every events observed.
// If a non-empty condition is provided, it will be updated in the status conditions for every handler execution
func RegisterSecretStatusHandler(ctx context.Context, controller SecretController, condition condition.Cond, name string, handler SecretStatusHandler) {
	statusHandler := &secretStatusHandler{
		client:    controller,
		condition: condition,
		handler:   handler,
	}
	controller.AddGenericHandler(ctx, name, generic.FromObjectHandlerToHandler(statusHandler.sync))
}

// RegisterSecretGeneratingHandler configures a SecretController to execute a SecretGeneratingHandler for every events observed, passing the returned objects to the provided apply.Apply.
// If a non-empty condition is provided, it will be updated in the status conditions for every handler execution
func RegisterSecretGeneratingHandler(ctx context.Context, controller SecretController, apply apply.Apply,
	condition condition.Cond, name string, handler SecretGeneratingHandler, opts *generic.GeneratingHandlerOptions) {
	statusHandler := &secretGeneratingHandler{
		SecretGeneratingHandler: handler,
		apply:                   apply,
		name:                    name,
		gvk:                     controller.GroupVersionKind(),
	}
	if opts != nil {
		statusHandler.opts = *opts
	}
	controller.OnChange(ctx, name, statusHandler.Remove)
	RegisterSecretStatusHandler(ctx, controller, condition, name, statusHandler.Handle)
}

type secretStatusHandler struct {
	client    SecretClient
	condition condition.Cond
	handler   SecretStatusHandler
}

// sync is executed on every resource addition or modification. Executes the configured handlers and sends the updated status to the Kubernetes API
func (a *secretStatusHandler) sync(key string, obj *v1alpha2.Secret) (*v1alpha2.Secret, error) {
	if obj == nil {
		return obj, nil
	}

	origStatus := obj.Status.DeepCopy()
	obj = obj.DeepCopy()
	newStatus, err := a.handler(obj, obj.Status)
	if err != nil {
		// Revert to old status on error
		newStatus = *origStatus.DeepCopy()
	}

	if a.condition != "" {
		if errors.IsConflict(err) {
			a.condition.SetError(&newStatus, "", nil)
		} else {
			a.condition.SetError(&newStatus, "", err)
		}
	}
	if !equality.Semantic.DeepEqual(origStatus, &newStatus) {
		if a.condition != "" {
			// Since status has changed, update the lastUpdatedTime
			a.condition.LastUpdated(&newStatus, time.Now().UTC().Format(time.RFC3339))
		}

		var newErr error
		obj.Status = newStatus
		newObj, newErr := a.client.UpdateStatus(obj)
		if err == nil {
			err = newErr
		}
		if newErr == nil {
			obj = newObj
		}
	}
	return obj, err
}

type secretGeneratingHandler struct {
	SecretGeneratingHandler
	apply apply.Apply
	opts  generic.GeneratingHandlerOptions
	gvk   schema.GroupVersionKind
	name  string
	seen  sync.Map
}

// Remove handles the observed deletion of a resource, cascade deleting every associated resource previously applied
func (a *secretGeneratingHandler) Remove(key string, obj *v1alpha2.Secret) (*v1alpha2.Secret, error) {
	if obj != nil {
		return obj, nil
	}

	obj = &v1alpha2.Secret{}
	obj.Namespace, obj.Name = kv.RSplit(key, "/")
	obj.SetGroupVersionKind(a.gvk)

	if a.opts.UniqueApplyForResourceVersion {
		a.seen.Delete(key)
	}

	return nil, generic.ConfigureApplyForObject(a.apply, obj, &a.opts).
		WithOwner(obj).
		WithSetID(a.name).
		ApplyObjects()
}

// Handle executes the configured SecretGeneratingHandler and pass the resulting objects to apply.Apply, finally returning the new status of the resource
func (a *secretGeneratingHandler) Handle(obj *v1alpha2.Secret, status v1alpha2.SecretStatus) (v1alpha2.SecretStatus, error) {
	if !obj.DeletionTimestamp.IsZero() {
		return status, nil
	}

	objs, newStatus, err := a.SecretGeneratingHandler(obj, status)
	if err != nil {
		return newStatus, err
	}
	if !a.isNewResourceVersion(obj) {
		return newStatus, nil
	}

	err = generic.ConfigureApplyForObject(a.apply, obj, &a.opts).
		WithOwner(obj).
		WithSetID(a.name).
		ApplyObjects(objs...)
	if err != nil {
		return newStatus, err
	}
	a.storeResourceVersion(obj)
	return newStatus, nil
}

// isNewResourceVersion detects if a specific resource version was already successfully processed.
// Only used if UniqueApplyForResourceVersion is set in generic.GeneratingHandlerOptions
func (a *secretGeneratingHandler) isNewResourceVersion(obj *v1alpha2.Secret) bool {
	if !a.opts.UniqueApplyForResourceVersion {
		return true
	}

	// Apply once per resource version
	key := obj.Namespace + "/" + obj.Name
	previous, ok := a.seen.Load(key)
	return !ok || previous != obj.ResourceVersion
}

// storeResourceVersion keeps track of the latest resource version of an object for which Apply was executed
// Only used if UniqueApplyForResourceVersion is set in generic.GeneratingHandlerOptions
func (a *secretGeneratingHandler) storeResourceVersion(obj *v1alpha2.Secret) {
	if !a.opts.UniqueApplyForResourceVersion {
		return
	}

	key := obj.Namespace + "/" + obj.Name
	a.seen.Store(key, obj.ResourceVersion)
}
