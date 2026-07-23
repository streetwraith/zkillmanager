<!-- BEGIN devskills:base -->
## 1. Think Before Coding

**State assumptions. Surface confusion and tradeoffs; don't pick silently.**

Before implementing:
- State your assumptions explicitly; when something's unclear, name it and ask.
- If multiple interpretations exist, present them — don't choose one silently.
- If a simpler approach exists, say so. Push back when warranted.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.
- Refactor overly long functions without being asked — length alone is a smell worth fixing, even when nothing else is wrong.
- **Comments target humans and explain WHY, not WHAT** — a non-obvious constraint, invariant, or workaround. Default to one line, only where the reason isn't clear from the code; never restate code or cite plan/ticket IDs. A comment past a few lines is rare and signals "this matters" — keep that signal meaningful.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match the codebase's **conventions** — naming, formatting, idioms — **not its deficiencies**. Write what you touch to standard; don't down-level new work to match surrounding code.
- If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

Make those tests count: behavior through the public interface, the failure modes that matter — not coverage, and never pinned to implementation.

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

## 5. Safe at the Boundaries

**Distrust the edges. Fail loudly, not silently.**

- Validate untrusted input where it enters — args, request payloads, external API responses. Don't trust it deep inside.
- Handle the errors that can actually happen; propagate or surface the rest. Never swallow an error to make a path look clean.

## 6. Retrieve Just-in-Time

**Pull context on demand. Locate before you read.**

- Search to find the right place; read scoped regions, not whole files "to be safe".
- If `PROJECT.md` exists, read its map first and prefer it over re-deriving structure. When the map and the code disagree, the code wins — reread the file.
- Delegate broad searches to a sub-agent where one is available, so the sweep stays out of your context.
- Sufficiency beats thrift: when unsure, read more. A wrong answer costs far more than the tokens.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.
<!-- END devskills:base -->

<!-- BEGIN devskills:language:go -->
<!-- profile: go — managed by devskills; edits between these markers are overwritten -->
## Language Profile — Go

Target: Go 1.24+. Backend services, CLIs, APIs, systems tooling.

Apply these conventions to all Go code in this session.

### Toolchain

Test with `go test -race ./...`. Lint with `golangci-lint run`. Benchmark with `go test -bench=. -benchmem`. Modernize with `go fix` — the canonical modernizer (rebuilt on the analysis framework), preferred over `gopls`/`golangci-lint modernize` for safe idiom upgrades. *(Go 1.26+)*

### Project Layout

`cmd/<name>/` for entrypoints, `internal/` for private packages, `pkg/` only for a real library, `api/` for protobuf/OpenAPI. Name packages by what they provide — avoid `util/`, `common/`, `helpers/`.

### Standard Library & Idioms

- Reach for `slices`, `maps`, and `cmp` over hand-rolled helpers; use the `min`/`max`/`clear` builtins. Range over integers with `for i := range n`.
- Expose sequences as `iter.Seq`/`iter.Seq2` (range-over-func) and prefer `slices.Sorted`/`slices.Collect`/`maps.Keys` over manual accumulation. *(Go 1.23+)*
- `json:",omitzero"` over `omitempty` when the intent is "omit the zero value" — it handles zero `time.Time` and zero structs correctly.
- Take a pointer to any expression with `new(expr)` — retires the hand-rolled `func ptr[T](v T) *T` helper for optional JSON/proto fields. *(Go 1.26+)*
- Self-bounded generics reference their own type-parameter list (`T Ordered[T]`) instead of an extra interface indirection, where it reads cleaner. *(Go 1.26+)*
- Heavy JSON paths may evaluate `encoding/json/v2` (`GOEXPERIMENT=jsonv2`) for correctness/perf wins — still experimental, don't require it in production. *(Go 1.26+)*

### Error Handling

- Wrap with context: `fmt.Errorf("operation: %w", err)`. Never discard an error with `_`.
- Sentinel errors (`var ErrNotFound = errors.New(...)`) checked with `errors.Is`, not `==`.

### Concurrency

- Every goroutine has an explicit exit condition. `context.Context` for cancellation, always the first parameter.
- `errgroup` for parallel work with error collection; `sync.WaitGroup` for fan-out, channels for coordination.
- Protect shared state with a named `sync.Mutex` field (conventionally first) — not embedded; embedding leaks `Lock`/`Unlock` into the struct's public API.

```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { return doWork(ctx) })
if err := g.Wait(); err != nil { ... }
```

### HTTP Services

Always set timeouts: `http.Server` gets `ReadTimeout`/`WriteTimeout`/`IdleTimeout`; `http.Client` gets `Timeout`. Use `net/http/httptest` for handler tests. When joining request-controlled input into filesystem paths, open a root with `os.OpenRoot` and use the `*os.Root` methods — they refuse traversal and symlink escapes, unlike manual `filepath.Clean`/prefix checks.

### Testing

- Table-driven tests with subtests (`t.Run`); `t.Helper()` in helpers.
- No real network or filesystem in unit tests — use interfaces and fakes.
- Integration tests behind `//go:build integration`.
- Benchmarks use `for b.Loop() { ... }`, not `for i := 0; i < b.N; i++` — setup outside the loop runs once and results stay live without `runtime.KeepAlive`.
- Test time-dependent or concurrent code with `testing/synctest` and its fake clock instead of real `time.Sleep`. *(Go 1.25+; `synctest.Test`)*

### Tiger Style

- Non-trivial functions assert their preconditions; validate length/nil on user-supplied slices and maps.
- No `panic` in library code (acceptable in `main` for configuration errors only).
<!-- END devskills:language:go -->
