/*
Copyright 2024.

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

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Masterminds/sprig/v3"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	corev1alpha1 "github.com/kaleido-io/paladin/operator/api/v1alpha1"
	"github.com/kaleido-io/paladin/toolkit/pkg/pldapi"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
)

// TransactionInvokeReconciler reconciles a TransactionInvoke object
type TransactionInvokeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// allows generic functions by giving a mapping between the types and interfaces for the CR
var TransactionInvokeCRMap = CRMap[corev1alpha1.TransactionInvoke, *corev1alpha1.TransactionInvoke, *corev1alpha1.TransactionInvokeList]{
	NewList: func() *corev1alpha1.TransactionInvokeList { return new(corev1alpha1.TransactionInvokeList) },
	ItemsFor: func(list *corev1alpha1.TransactionInvokeList) []corev1alpha1.TransactionInvoke {
		return list.Items
	},
	AsObject: func(item *corev1alpha1.TransactionInvoke) *corev1alpha1.TransactionInvoke { return item },
}

// +kubebuilder:rbac:groups=core.paladin.io,resources=transactioninvokes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.paladin.io,resources=transactioninvokes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.paladin.io,resources=transactioninvokes/finalizers,verbs=update

func (r *TransactionInvokeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// TODO: Add an admission webhook to make the contract deps, bytecode and ABIs immutable

	// Fetch the TransactionInvoke instance
	var txi corev1alpha1.TransactionInvoke
	if err := r.Get(ctx, req.NamespacedName, &txi); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get TransactionInvoke resource")
		return ctrl.Result{}, err
	}

	// Check all our deps are resolved
	depsChanged, ready, err := checkSmartContractDeps(ctx, r.Client, txi.Namespace, txi.Spec.ContractDeploymentDeps, &txi.Status.ContactDependenciesStatus)
	if err != nil {
		return ctrl.Result{}, err
	} else if depsChanged {
		return r.updateStatusAndRequeue(ctx, &txi)
	} else if !ready {
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Reconcile the deployment transaction
	txReconcile := newTransactionReconcile(r.Client,
		"txinvoke."+txi.Name,
		txi.Spec.Node, txi.Namespace,
		&txi.Status.TransactionSubmission,
		func() (bool, *pldapi.TransactionInput, error) { return r.buildDeployTransaction(&txi) },
	)
	err = txReconcile.reconcile(ctx)
	if err != nil {
		// There's nothing to notify us when the world changes other than polling, so we keep re-trying
		return ctrl.Result{}, err
	} else if txReconcile.statusChanged {
		return r.updateStatusAndRequeue(ctx, &txi)
	} else if !txReconcile.failed && !txReconcile.succeeded {
		return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
	}
	// Nothing left to do - we succeeded or failed
	return ctrl.Result{}, nil
}

func (r *TransactionInvokeReconciler) updateStatusAndRequeue(ctx context.Context, txi *corev1alpha1.TransactionInvoke) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, txi); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update smart contract deployment status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil // Run again immediately to submit
}

func (r *TransactionInvokeReconciler) buildDeployTransaction(txi *corev1alpha1.TransactionInvoke) (bool, *pldapi.TransactionInput, error) {

	toTemplate, err := template.New("").Option("missingkey=error").Funcs(sprig.FuncMap()).Parse(txi.Spec.ToTemplate)
	if err != nil {
		return false, nil, fmt.Errorf("toTemplate invalid: %s", err)
	}
	paramsTemplate, err := template.New("").Option("missingkey=error").Funcs(sprig.FuncMap()).Parse(txi.Spec.ParamsJSONTemplate)
	if err != nil {
		return false, nil, fmt.Errorf("paramsJSONTemplate invalid: %s", err)
	}

	var crMap map[string]any
	crJSON, err := json.Marshal(txi)
	if err == nil {
		err = json.Unmarshal(crJSON, &crMap)
	}
	if err != nil {
		return false, nil, err
	}

	toBuff := new(strings.Builder)
	if err = toTemplate.Execute(toBuff, crMap); err != nil {
		return false, nil, fmt.Errorf("toTemplate failed: %s", err)
	}
	to, err := tktypes.ParseEthAddress(toBuff.String())
	if err != nil {
		return false, nil, fmt.Errorf("toTemplate result '%s' not a valid address: %s", toBuff, err)
	}

	paramsJSONBuff := new(bytes.Buffer)
	if err = paramsTemplate.Execute(paramsJSONBuff, crMap); err != nil {
		return false, nil, fmt.Errorf("ar paramsJSONTemplate: %s", err)
	}

	var a abi.ABI
	if err := json.Unmarshal([]byte(txi.Spec.ABIJSON), &a); err != nil {
		return false, nil, fmt.Errorf("invalid ABI: %s", err)
	}

	return true, &pldapi.TransactionInput{
		TransactionBase: pldapi.TransactionBase{
			Type:   tktypes.Enum[pldapi.TransactionType](txi.Spec.TxType),
			Domain: txi.Spec.Domain,
			From:   txi.Spec.From,
			To:     to,
			Data:   paramsJSONBuff.Bytes(),
		},
		ABI: a,
	}, nil

}

func checkSmartContractDeps(ctx context.Context, c client.Client, namespace string, requiredContractDeployments []string, pStatus *corev1alpha1.ContactDependenciesStatus) (bool, bool, error) {

	if pStatus.ContractDepsSummary != "" &&
		len(pStatus.ResolvedContractAddresses) == len(requiredContractDeployments) {
		// We have persisted the status change with the full dependencies reconciled
		return false, true, nil
	}

	// If our status has changed if we've never built it before, or we find a missing dep below
	statusChanged := false
	if pStatus.ResolvedContractAddresses == nil {
		pStatus.ResolvedContractAddresses = make(map[string]string)
		statusChanged = true
	}

	// Look for any missing deps
	for _, dep := range requiredContractDeployments {
		if pStatus.ResolvedContractAddresses[dep] == "" {
			contractAddress, err := getContractDeploymentAddress(ctx, c, dep, namespace)
			if err != nil {
				return false, false, err
			}
			if contractAddress != "" {
				statusChanged = true
				pStatus.ResolvedContractAddresses[dep] = contractAddress
			}
		}
	}

	// Rebuild this string every time, but statusChanged calc'd above decides if we store it back
	pStatus.ContractDepsSummary = fmt.Sprintf("%d/%d", len(pStatus.ResolvedContractAddresses), len(requiredContractDeployments))
	return statusChanged, false /* only return true once we've persisted the status change */, nil

}

func getContractDeploymentAddress(ctx context.Context, c client.Client, name, namespace string) (string, error) {

	var scd corev1alpha1.SmartContractDeployment
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &scd)
	if err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return scd.Status.ContractAddress, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *TransactionInvokeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.TransactionInvoke{}).
		// Reconcile when any node status changes
		Watches(&corev1alpha1.Paladin{}, reconcileAll(TransactionInvokeCRMap, r.Client), reconcileEveryChange()).
		// Reconcile when any smart contract deploy changes
		Watches(&corev1alpha1.SmartContractDeployment{}, reconcileAll(TransactionInvokeCRMap, r.Client), reconcileEveryChange()).
		Complete(r)
}