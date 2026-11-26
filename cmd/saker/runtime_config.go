package main

import (
	"path/filepath"

	"github.com/kksty/saker/internal/settings"
)

// runtimeHarnessArgs is shared by boot and settings-driven restarts. Passing an
// explicit, filtered document prevents the harness from discovering and parsing
// the operator's file, where Saker keeps metadata such as profile pause state.
//
// There are two documents because the two harness processes read different
// sections: the scan declares the product sections (mcp/search/guardrail/jev)
// and reads the full file; the Hub's strict validator rejects sections it does
// not declare, so it reads the shared-only projection. Both are generated from
// the same operator file, so a save reaches both without a second source.
func runtimeHarnessArgs(st *settings.Store, dataDir string, m settings.Model) (hubArgs, scanArgs []string, err error) {
	dir := filepath.Join(dataDir, "host", "config")
	scanPath, err := st.RuntimeConfig(dir)
	if err != nil {
		return nil, nil, err
	}
	hubPath, err := st.SharedRuntimeConfig(dir)
	if err != nil {
		return nil, nil, err
	}
	modelArgs := m.Args()
	return append([]string{"--config", hubPath}, modelArgs...), append([]string{"--config", scanPath}, modelArgs...), nil
}
