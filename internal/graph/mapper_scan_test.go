package graph

// Thin views over scanMapperFiles for tests that exercise one extraction.

func scanSymbols(files []mapperFile) ([]mapperSym, mapperStats) {
	r := scanMapperFiles(files)
	return r.syms, r.stats
}

func scanCalls(files []mapperFile) ([]mapperFileCalls, mapperStats) {
	r := scanMapperFiles(files)
	return r.fileCalls, r.stats
}

func scanImports(files []mapperFile) ([]importRow, mapperImportBindings, mapperStats) {
	r := scanMapperFiles(files)
	return r.importRows, r.bindings, r.stats
}
