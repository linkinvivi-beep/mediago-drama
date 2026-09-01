//go:build darwin && cgo

package keychain

import "testing"

func TestProductionGenericPasswordStoreDoesNotUseSecurityCLI(t *testing.T) {
	if _, ok := NewGenericPasswordStore().(*genericPasswordStore); ok {
		t.Fatal("NewGenericPasswordStore() uses security(1); background writes can persist an empty password")
	}
}
