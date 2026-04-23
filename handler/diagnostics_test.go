package handler

// Pull-diagnostic tests used to live here. We stopped advertising
// textDocument/diagnostic (the pull model) because qmllint is async and
// clients that pull expect a synchronous, authoritative answer we can't
// provide — they'd overwrite our debounced push results with whatever the
// pull returned (empty). Diagnostics are published via
// textDocument/publishDiagnostics and are covered by TestQmllintRunnerEndToEnd
// in qmllint_test.go.
