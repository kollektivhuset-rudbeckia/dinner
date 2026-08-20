package mail

import "encoding/base64"

// base64Decode is a small test helper kept beside the tests that use it.
func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
