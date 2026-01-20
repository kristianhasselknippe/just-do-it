# just-do-it

A TUI (Text User Interface) for [just](https://github.com/casey/just) task runner.

## Features

- **Auto-Discovery**: Uses `just` to load tasks from your `justfile`.
- **Search**: Type to filter tasks instantly.
- **Inspect**: View task commands and dependencies in a side panel.
- **Run**: Execute tasks interactively (supports full shell access, e.g., `git commit`, `vim`, etc.).

## Installation

```bash
go install github.com/yourusername/just-do-it@latest
# OR build from source
go build -o just-do-it
```

## Usage

Simply run `just-do-it` in a directory containing a `justfile`.

```bash
./just-do-it
```

### Controls

- **Arrow Keys / j/k**: Navigate the list.
- **Type**: Filter/Search tasks.
- **Enter**: Run the selected task.
- **Esc**: Clear filter or Quit.
- **q / Ctrl+C**: Quit.
