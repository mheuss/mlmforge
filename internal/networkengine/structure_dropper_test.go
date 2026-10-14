package networkengine

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTreeMutatorFamilyDoesNotCarryDropStructure(t *testing.T) {
	for _, iface := range []reflect.Type{
		reflect.TypeFor[TreeMutator](),
		reflect.TypeFor[TreeEngine](),
		reflect.TypeFor[TreeEngineChecker](),
	} {
		_, found := iface.MethodByName("DropStructure")
		assert.False(t, found, "%s carries DropStructure", iface.Name())
	}
}
