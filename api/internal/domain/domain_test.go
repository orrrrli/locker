package domain

import (
	"go/build"
	"strings"
	"testing"
)

const module = "github.com/orrrrli/locker/api"

func TestDomainImportsNothingFromModule(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, module) {
			t.Errorf("domain imports %q; domain must not import other packages from this module", imp)
		}
	}
}
