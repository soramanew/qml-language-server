# QML Language Server

A Go-based Language Server for QML (Qt Meta-Object Language) that provides intelligent code editing features.

This is a fork of [cushycush/qml-language-server](https://github.com/cushycush/qml-language-server).

> [!NOTE]
> This is entirely vibe coded.
> It was built because `qmlls` sucks, and I needed something better (yes, I consider an entirely vibe coded
> LSP is better than `qmlls`).
> This also has custom integration with the Caelestia codebase (the custom lint scripts from it).

## Installation

### Build from Source

Requires Go 1.26.1+.

```bash
git clone https://github.com/soramanew/qml-language-server.git
cd qml-language-server
make build
```

`make install` will build the binary and copy it to `~/.local/bin`.

## Editor Configuration

### VS Code

1. Install the "Local LSP" extension or create a custom extension
2. Add to your settings.json:

```json
{
  "languageServers": {
    "qml": {
      "command": "qml-language-server",
      "filetypes": ["qml"]
    }
  }
}
```

### Neovim

For Neovim 0.11+, use the built-in LSP configuration:

```lua
vim.lsp.config.qml = {
  name = "qml-language-server",
  filetypes = { "qml" },
  root_dir = function(fname)
    local root_patterns = { '.git', '.qml', 'qmldir' }
    for _, pattern in ipairs(root_patterns) do
      local root = vim.fs.find(pattern, { path = fname, upward = true })[1]
      if root then
        return vim.fs.dirname(root)
      end
    end
    return vim.fs.dirname(fname)
  end,
  cmd = { "qml-language-server" },
}

vim.lsp.enable("qml")
```

For Neovim 0.10 and earlier, use `lspconfig`:

```lua
local lspconfig = require('lspconfig')

lspconfig.qmlls.setup {
  cmd = { "qml-language-server" },
  filetypes = { "qml" },
  root_dir = function(fname)
    return lspconfig.util.find_git_roots(fname) or lspconfig.util.find_root({ '*.qml' }, fname)
  end,
}
```

### Neovim with blink.cmp

For a modern completion experience with fuzzy matching and snippets, use [blink.cmp](https://github.com/saghen/blink.cmp):

```lua
{
  'saghen/blink.cmp',
  opts = {
    sources = {
      default = { 'lsp' },
    },
    completion = {
      documentation = {
        auto_show = true,
      },
    },
  },
}
```

## Development

```bash
make test       # run tests with race detector
make lint       # golangci-lint
make coverage   # generate coverage report
make build      # compile binary
```

### Project Dependencies

- [go-lsp](https://github.com/owenrumney/go-lsp) - LSP protocol implementation
- [gotreesitter](https://github.com/odvcencio/gotreesitter) - Pure-Go tree-sitter runtime

## License

MIT License - See LICENSE file for details

## Contributing

Contributions welcome! Please open an issue or submit a pull request.

## Acknowledgments

- [gotreesitter](https://github.com/odvcencio/gotreesitter) - Pure-Go tree-sitter runtime
- [tree-sitter-qmljs](https://github.com/yuja/tree-sitter-qmljs) - QML grammar for tree-sitter
- [go-lsp](https://github.com/owenrumney/go-lsp) - Go LSP library
