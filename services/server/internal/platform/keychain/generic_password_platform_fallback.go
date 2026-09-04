//go:build !darwin || !cgo

package keychain

func newPlatformGenericPasswordStore() GenericPasswordStore {
	return newGenericPasswordStore(execRunner{factory: execSecurityCommandFactory{}})
}
