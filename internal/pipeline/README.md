# pipeline

A small Go library that runs commands written as configuration: typed,
versioned modules connected step by step, each run saved and resumable, with
side effects only through a gate the host controls. It imports nothing but
the standard library (a test enforces this). SmsBot is its first host.

## Model

- **Trigger**: a kind of event that starts a run (a chat command, a schedule,
  a webhook…) and the outputs it provides. The host registers trigger kinds.
- **Module**: a registered capability with a stable id and version, typed
  input and output ports, a strict settings schema, and an effect class:
  `pure`, `local` (works on the host), `paid` (costs money) or `sink` (acts
  on the outside world).
- **Pipeline**: configuration: a trigger plus a linear list of steps. Each
  step runs one module; each input connects to `trigger.<output>` or an
  earlier `<step>.<output>`.
- **Value types**: registered by the host. A type can count as others (`Is`),
  so an input taking `file` can take a `video`. Values carry text, a handle
  to something the host stores (never a path), or host-defined data.

## Guarantees

1. **Checked when saved.** `Validate` applies registered upgrades, then checks
   ids, modules and versions, earlier-only and type-compatible connections,
   required inputs, settings (strictly, via each module) and the host's
   policy and extra rules.
2. **Snapshots.** `Snapshot` fixes the recipe, versions and settings into a
   run; editing the command never changes it.
3. **Resumable.** A `Run` is one JSON document. `Accept` runs admission and
   every step that can finish when the event arrives; `Resume` runs the rest,
   and any step can `Yield` and be resumed later from where it stopped.
4. **Gated effects.** A sink step's connected inputs are its intent. The
   engine hands it to `Host.Effect`, which carries it out or refuses, unless
   `Policy.Direct` lets that module act itself.
5. **Composition only.** Configuration connects registered modules; new
   capabilities are reviewed code.

## Host contract

```go
e := pipeline.New[MyCtx](pipeline.Policy{
	MaxSteps: 8, MaxEffects: 1, EffectLast: true, RequireEffect: true,
	DefaultTrigger: "message",
	Direct: func(module string) bool { return false }, // every effect gated
	Check:  myExtraRules,                              // host-specific checks
})
e.RegisterType(...); e.RegisterTrigger(...); e.Register(module); e.RegisterUpgrade(...)

err := e.Validate(&recipe, env)                 // on save; env reaches module settings
run := pipeline.Snapshot(recipe, triggerOutputs) // per event
result, err := e.Accept(ctx, run, host)          // Declined, Ready or Halted
stopped, err := e.Resume(ctx, run, host)         // later, until !stopped
trace, halt, err := e.Simulate(ctx, run)         // offline preview
```

`MyCtx` is whatever per-run context the host's modules need; the engine only
passes it through. The host implements:

| Method | Called when |
|---|---|
| `Effect(step)` | a sink step finished; its inputs are the intent |
| `Halt(step, value)` | a step ended the run early with a value |
| `Save()` | a step finished during `Resume`; persist the run |
| `Fail(code)` | the run failed (a module failed, or is no longer registered) |

Module calls return an `Outcome`: `Done`, `Pending` (at acceptance: run
later), `Halt(value)`, `Decline` (at acceptance: don't run at all), `Yield`
(the module arranged with the host to be resumed) or `Fail(code)`.

See `example_test.go` for a complete host in a few lines, and
`internal/bot/pipeline.go` for SmsBot's.
