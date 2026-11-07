# widgetd: a self-hostable host for sandboxed live widgets.

mise := "mise exec --"

# Everything a fresh clone needs
setup: generate client-build examples

# JS deps (also provides protoc-gen-es)
install:
    {{mise}} bun install --frozen-lockfile

# Go + TS for widgetd's own protos, and the test widget's
generate: install
    {{mise}} buf generate
    cd internal/testwidget && {{mise}} buf generate

# Bundle <widget-board> and the admin pages into client/dist (committed:
# the Go module embeds it, and its users have no JS toolchain)
client-build:
    cd client && {{mise}} bun x tsc -p .
    cd client && {{mise}} bun build src/board.ts src/admin.ts --outdir dist --target browser --format esm --minify
    cp client/src/admin.css client/dist/admin.css
    touch client/dist/.gitkeep

# Build the example widgets (schema codegen + view bundle + widget_gen.go)
examples:
    {{mise}} go run ./cmd/widgetc build examples/bitcoin-spiral

# The full check suite
test: generate client-build examples
    cd client && {{mise}} bun test
    cd sdk/web && {{mise}} bun test
    cd examples/bitcoin-spiral/view && {{mise}} bun test
    {{mise}} go vet ./...
    {{mise}} go test -race ./...

# Run the standalone server on :8080
run *ARGS:
    {{mise}} go run ./cmd/widgetd {{ARGS}}

build:
    {{mise}} go build -trimpath -o bin/widgetd ./cmd/widgetd
