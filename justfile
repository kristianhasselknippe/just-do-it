# Justfile for testing just-do-it

# Lists files in the current directory
ls:
    ls -F

# Prints a greeting
hello name="World":
    echo "Hello, {{name}}!"

# Builds the project
build:
    @echo "Building just-do-it..."
    go build -o just-do-it
    @echo "Build complete!"

# Installs the binary to $GOPATH/bin
install:
    @echo "Installing just-do-it..."
    go install
    @echo "Install complete!"

# Deploys the project
deploy: build
    @echo "Deploying..."

# Runs an interactive shell (to test TUI suspension)
shell:
    @echo "Entering shell... (type exit to return)"
    bash

# Diffs two files (good for testing multiple args + file picker)
diff file1 file2:
    diff {{file1}} {{file2}}
