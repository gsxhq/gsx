package codegen

import (
	"go/types"
	"runtime"
)

func testTypeCheckEnvironment() typeCheckEnvironment {
	return typeCheckEnvironment{
		sizes:     types.SizesFor("gc", runtime.GOARCH),
		goVersion: "go1.26",
	}
}

func testBundle(imp types.Importer, table funcTables) *Bundle {
	environment := testTypeCheckEnvironment()
	return &Bundle{imp: imp, table: table, sizes: environment.sizes, goVersion: environment.goVersion}
}
