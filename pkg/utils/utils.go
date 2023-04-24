package utils

import (
	"context"
	"fmt"
	"github.com/barkimedes/go-deepcopy"
	pkgerrors "github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetOrCreateObject(cl client.Client, expected client.Object, log logrus.FieldLogger) (client.Object, error) {
	namespacedName := types.NamespacedName{
		Name:      expected.GetName(),
		Namespace: expected.GetNamespace(),
	}

	obj := deepcopy.MustAnything(expected).(client.Object)
	err := cl.Get(context.TODO(), namespacedName, obj)
	if errors.IsNotFound(err) {
		msgSuffix := fmt.Sprintf("object of type %+v with name %s in namespace %s", expected.GetObjectKind(), expected.GetName(), expected.GetNamespace())
		if err = cl.Create(context.TODO(), expected); err != nil {
			return nil, pkgerrors.Wrapf(err, msgSuffix)
		}
		log.Infof("Created %s", msgSuffix)
		obj = expected
	}

	return obj, err
}

func DeleteObject(cl client.Client, obj client.Object) error {
	err := cl.Delete(context.TODO(), obj)
	return client.IgnoreNotFound(err)
}
