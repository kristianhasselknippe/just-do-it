# Justfile for testing just-ui

# Lists files in the current directory
ls:
    ls -F

# Prints a greeting
hello name="World":
    echo "Hello, {{name}}!"

# Builds the project (mock)
build:
    @echo "Building..."
    sleep 1
    @echo "Done!"

# Deploys the project
deploy: build
    @echo "Deploying..."

# Runs an interactive shell (to test TUI suspension)
shell:
    @echo "Entering shell... (type exit to return)"
    bash
