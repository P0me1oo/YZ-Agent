package kernel

import (
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestUserDiffReplacesChangedUUID(t *testing.T) {
	oldUsers := []model.UserSpec{
		{ID: 15, UUID: "uuid-old", SpeedLimit: 8},
		{ID: 20, UUID: "uuid-unchanged", SpeedLimit: 4},
	}
	newUsers := []model.UserSpec{
		{ID: 15, UUID: "uuid-new", SpeedLimit: 8},
		{ID: 20, UUID: "uuid-unchanged", SpeedLimit: 4},
	}

	toAdd, toRemove := UserDiff(oldUsers, newUsers)

	if want := []model.UserSpec{newUsers[0]}; !reflect.DeepEqual(toAdd, want) {
		t.Fatalf("toAdd = %#v, want %#v", toAdd, want)
	}
	if want := []model.UserSpec{oldUsers[0]}; !reflect.DeepEqual(toRemove, want) {
		t.Fatalf("toRemove = %#v, want %#v", toRemove, want)
	}
}

func TestUserDiffIgnoresMetadataOnlyChanges(t *testing.T) {
	oldUsers := []model.UserSpec{{ID: 15, UUID: "uuid-same", SpeedLimit: 8, DeviceLimit: 1}}
	newUsers := []model.UserSpec{{ID: 15, UUID: "uuid-same", SpeedLimit: 16, DeviceLimit: 2}}

	toAdd, toRemove := UserDiff(oldUsers, newUsers)

	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Fatalf("metadata-only change produced add/remove: add=%#v remove=%#v", toAdd, toRemove)
	}
}
