// SPDX-License-Identifier: MIT

package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waya0125/recfriio/internal/bcas"
)

type tunerWithoutBuiltinAccess struct{ tuner }

func (tunerWithoutBuiltinAccess) BuiltinCard(io.Writer) bcas.Transport {
	panic("外付け指定で内蔵カードにアクセスした")
}

func TestPCSCBypassesBuiltinCard(t *testing.T) {
	t.Setenv("PCSCLITE_CSOCK_NAME", filepath.Join(t.TempDir(), "pcscd"))
	_, err := newDecoder(&options{b25: true, pcsc: true}, tunerWithoutBuiltinAccess{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pcscd") {
		t.Fatalf("PC/SCソケットへの接続エラーを期待: %v", err)
	}
}
