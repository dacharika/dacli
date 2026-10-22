//go:build darwin && cgo

package credentialstore

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef dacli_query(const char *account) {
 CFMutableDictionaryRef q=CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 CFStringRef a=CFStringCreateWithCString(NULL,account,kCFStringEncodingUTF8);
 if(!q || !a){if(q)CFRelease(q);if(a)CFRelease(a);return NULL;}
 CFDictionarySetValue(q,kSecClass,kSecClassGenericPassword);
 CFDictionarySetValue(q,kSecAttrService,CFSTR("org.dacli.device-identity.v1"));
 CFDictionarySetValue(q,kSecAttrAccount,a);CFRelease(a);return q;
}
static OSStatus dacli_store_put(const char *key,const unsigned char *bytes,long size){
 CFMutableDictionaryRef q=dacli_query(key);if(!q)return errSecAllocate;
 CFDataRef data=CFDataCreate(NULL,bytes,size);if(!data){CFRelease(q);return errSecAllocate;}
 CFMutableDictionaryRef changes=CFDictionaryCreateMutable(NULL,0,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
 if(!changes){CFRelease(data);CFRelease(q);return errSecAllocate;}
 CFDictionarySetValue(changes,kSecValueData,data);
 OSStatus result=SecItemUpdate(q,changes);
 if(result==errSecItemNotFound){CFDictionarySetValue(q,kSecValueData,data);result=SecItemAdd(q,NULL);}
 CFRelease(changes);CFRelease(data);CFRelease(q);return result;
}
static OSStatus dacli_store_get(const char *key,unsigned char **bytes,long *size){
 CFMutableDictionaryRef q=dacli_query(key);if(!q)return errSecAllocate;
 CFDictionarySetValue(q,kSecReturnData,kCFBooleanTrue);CFDictionarySetValue(q,kSecMatchLimit,kSecMatchLimitOne);
 CFTypeRef found=NULL;OSStatus result=SecItemCopyMatching(q,&found);CFRelease(q);
 if(result!=errSecSuccess)return result;
 if(!found || CFGetTypeID(found)!=CFDataGetTypeID()){if(found)CFRelease(found);return errSecDecode;}
 CFIndex n=CFDataGetLength((CFDataRef)found);
 if(n<=0 || n>65536){CFRelease(found);return errSecDecode;}
 *bytes=(unsigned char *)malloc(n);if(!*bytes){CFRelease(found);return errSecAllocate;}
 memcpy(*bytes,CFDataGetBytePtr((CFDataRef)found),n);*size=n;CFRelease(found);return errSecSuccess;
}
static OSStatus dacli_store_delete(const char *key){CFMutableDictionaryRef q=dacli_query(key);if(!q)return errSecAllocate;OSStatus result=SecItemDelete(q);CFRelease(q);return result;}
*/
import "C"
import (
	"strings"
	"unsafe"
)

type keychainStore struct{}

func platformStore() (Store, error) { return keychainStore{}, nil }
func keyAllowed(k string) bool      { return len(k) == 64 && strings.Trim(k, "0123456789abcdef") == "" }
func (keychainStore) Put(key string, value []byte) error {
	if !keyAllowed(key) || len(value) == 0 || len(value) > MaxCredentialBytes {
		return ErrDenied
	}
	k := C.CString(key)
	defer C.free(unsafe.Pointer(k))
	if C.dacli_store_put(k, (*C.uchar)(unsafe.Pointer(&value[0])), C.long(len(value))) != C.errSecSuccess {
		return ErrDenied
	}
	return nil
}
func (keychainStore) Get(key string) ([]byte, error) {
	if !keyAllowed(key) {
		return nil, ErrDenied
	}
	k := C.CString(key)
	defer C.free(unsafe.Pointer(k))
	var bytes *C.uchar
	var size C.long
	status := C.dacli_store_get(k, &bytes, &size)
	if status == C.errSecItemNotFound {
		return nil, ErrMissing
	}
	if status != C.errSecSuccess {
		return nil, ErrDenied
	}
	result := C.GoBytes(unsafe.Pointer(bytes), C.int(size))
	C.memset(unsafe.Pointer(bytes), 0, C.size_t(size))
	C.free(unsafe.Pointer(bytes))
	return result, nil
}
func (keychainStore) Delete(key string) error {
	if !keyAllowed(key) {
		return ErrDenied
	}
	k := C.CString(key)
	defer C.free(unsafe.Pointer(k))
	status := C.dacli_store_delete(k)
	if status != C.errSecSuccess && status != C.errSecItemNotFound {
		return ErrDenied
	}
	return nil
}
