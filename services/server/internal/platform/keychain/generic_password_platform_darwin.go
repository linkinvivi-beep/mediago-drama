//go:build darwin && cgo

package keychain

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation

#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

// These APIs deliberately target the login keychain used by security(1), so
// existing MediaLink items remain readable without a data-protection access
// group entitlement. Apple deprecates them in favor of SecItem on macOS, but
// the replacement changes the keychain domain and is not migration-compatible.
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

static OSStatus medialink_keychain_set(
	const void *service, UInt32 serviceLength,
	const void *account, UInt32 accountLength,
	const void *secret, UInt32 secretLength
) {
	SecKeychainItemRef item = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL, serviceLength, service, accountLength, account,
		NULL, NULL, &item
	);
	if (status == errSecSuccess) {
		status = SecKeychainItemModifyAttributesAndData(item, NULL, secretLength, secret);
		CFRelease(item);
		return status;
	}
	if (status != errSecItemNotFound) {
		return status;
	}
	return SecKeychainAddGenericPassword(
		NULL, serviceLength, service, accountLength, account,
		secretLength, secret, NULL
	);
}

static OSStatus medialink_keychain_get(
	const void *service, UInt32 serviceLength,
	const void *account, UInt32 accountLength,
	void **secretOut, UInt32 *secretLengthOut
) {
	void *keychainData = NULL;
	UInt32 keychainLength = 0;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL, serviceLength, service, accountLength, account,
		&keychainLength, &keychainData, NULL
	);
	if (status != errSecSuccess) {
		return status;
	}
	void *copy = malloc(keychainLength == 0 ? 1 : keychainLength);
	if (copy == NULL) {
		SecKeychainItemFreeContent(NULL, keychainData);
		return errSecAllocate;
	}
	if (keychainLength > 0) {
		memcpy(copy, keychainData, keychainLength);
	}
	SecKeychainItemFreeContent(NULL, keychainData);
	*secretOut = copy;
	*secretLengthOut = keychainLength;
	return errSecSuccess;
}

static OSStatus medialink_keychain_exists(
	const void *service, UInt32 serviceLength,
	const void *account, UInt32 accountLength,
	Boolean *existsOut
) {
	SecKeychainItemRef item = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL, serviceLength, service, accountLength, account,
		NULL, NULL, &item
	);
	if (status == errSecItemNotFound) {
		*existsOut = false;
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		return status;
	}
	CFRelease(item);
	*existsOut = true;
	return errSecSuccess;
}

static OSStatus medialink_keychain_delete(
	const void *service, UInt32 serviceLength,
	const void *account, UInt32 accountLength
) {
	SecKeychainItemRef item = NULL;
	OSStatus status = SecKeychainFindGenericPassword(
		NULL, serviceLength, service, accountLength, account,
		NULL, NULL, &item
	);
	if (status == errSecItemNotFound) {
		return errSecSuccess;
	}
	if (status != errSecSuccess) {
		return status;
	}
	status = SecKeychainItemDelete(item);
	CFRelease(item);
	return status;
}

static void medialink_clear_and_free(void *data, UInt32 length) {
	if (data == NULL) {
		return;
	}
	volatile unsigned char *cursor = (volatile unsigned char *)data;
	for (UInt32 index = 0; index < length; index++) {
		cursor[index] = 0;
	}
	free(data);
}

#pragma clang diagnostic pop
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"
)

type nativeGenericPasswordStore struct{}

func newPlatformGenericPasswordStore() GenericPasswordStore {
	return &nativeGenericPasswordStore{}
}

func (store *nativeGenericPasswordStore) Set(ctx context.Context, service, account, secret string) error {
	if err := validateNativeCall(ctx, store, service, account); err != nil {
		return err
	}
	if !validSecret(secret) {
		return fmt.Errorf("invalid generic password secret")
	}
	serviceData := C.CBytes([]byte(service))
	accountData := C.CBytes([]byte(account))
	secretData := C.CBytes([]byte(secret))
	defer C.free(serviceData)
	defer C.free(accountData)
	defer C.medialink_clear_and_free(secretData, C.UInt32(len(secret)))
	status := C.medialink_keychain_set(
		serviceData, C.UInt32(len(service)),
		accountData, C.UInt32(len(account)),
		secretData, C.UInt32(len(secret)),
	)
	if status != C.errSecSuccess {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("setting generic password: %w", ErrUnavailable)
	}
	return nil
}

func (store *nativeGenericPasswordStore) Get(ctx context.Context, service, account string) (string, error) {
	if err := validateNativeCall(ctx, store, service, account); err != nil {
		return "", err
	}
	serviceData := C.CBytes([]byte(service))
	accountData := C.CBytes([]byte(account))
	defer C.free(serviceData)
	defer C.free(accountData)
	var secretData unsafe.Pointer
	var secretLength C.UInt32
	status := C.medialink_keychain_get(
		serviceData, C.UInt32(len(service)),
		accountData, C.UInt32(len(account)),
		&secretData, &secretLength,
	)
	if status == C.errSecItemNotFound {
		return "", ErrNotFound
	}
	if status != C.errSecSuccess {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("reading generic password: %w", ErrUnavailable)
	}
	defer C.medialink_clear_and_free(secretData, secretLength)
	bytes := C.GoBytes(secretData, C.int(secretLength))
	defer clear(bytes)
	return string(bytes), nil
}

func (store *nativeGenericPasswordStore) Exists(ctx context.Context, service, account string) (bool, error) {
	if err := validateNativeCall(ctx, store, service, account); err != nil {
		return false, err
	}
	serviceData := C.CBytes([]byte(service))
	accountData := C.CBytes([]byte(account))
	defer C.free(serviceData)
	defer C.free(accountData)
	var exists C.Boolean
	status := C.medialink_keychain_exists(
		serviceData, C.UInt32(len(service)),
		accountData, C.UInt32(len(account)),
		&exists,
	)
	if status != C.errSecSuccess {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, fmt.Errorf("checking generic password: %w", ErrUnavailable)
	}
	return exists != 0, nil
}

func (store *nativeGenericPasswordStore) Delete(ctx context.Context, service, account string) error {
	if err := validateNativeCall(ctx, store, service, account); err != nil {
		return err
	}
	serviceData := C.CBytes([]byte(service))
	accountData := C.CBytes([]byte(account))
	defer C.free(serviceData)
	defer C.free(accountData)
	status := C.medialink_keychain_delete(
		serviceData, C.UInt32(len(service)),
		accountData, C.UInt32(len(account)),
	)
	if status != C.errSecSuccess {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("deleting generic password: %w", ErrUnavailable)
	}
	return nil
}

func validateNativeCall(ctx context.Context, store *nativeGenericPasswordStore, service, account string) error {
	if ctx == nil {
		return fmt.Errorf("generic password context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("generic password store is unavailable: %w", ErrUnavailable)
	}
	if !validIdentifier(service) || !validIdentifier(account) {
		return fmt.Errorf("invalid generic password identifier")
	}
	return nil
}
