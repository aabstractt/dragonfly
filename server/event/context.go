package event

// Context represents the context of an event. Handlers of an event may call methods on the context to change
// the result of the event.
type Context struct {
	cancel   bool
	finished bool
	after    []func(bool)
}

// C returns a new event context.
func C() *Context {
	return &Context{}
}

// Cancelled returns whether the context has been cancelled.
func (ctx *Context) Cancelled() bool {
	return ctx.cancel
}

// Cancel cancels the context.
func (ctx *Context) Cancel() {
	ctx.cancel = true
}

// After adds a function to the Context to be executed
func (ctx *Context) After(f func(cancelled bool)) {
	ctx.after = append(ctx.after, f)
}

// Finish marks the Context as finished, running all After functions.
func (ctx *Context) Finish() {
	if ctx.finished {
		panic("Context.Finish was already called")
	}
	ctx.finished = true
	for _, f := range ctx.after {
		f(ctx.cancel)
	}
	ctx.after = nil
}
