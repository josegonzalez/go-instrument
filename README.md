# go-instrument

This package exposes a simple and opinionated library for instrumenting go apps using InfluxDB and NewRelic.
At its core, it is just a struct holding references to both services and exposing an unified API to deliver
metrics to both services at once

## Installation

Install it using the `go get` command:

    go get github.com/seatgeek/go-instrument

## Usage

The simplest way to use this library is by getting a default `InstrumentsConfig` using the `DefaultConfig()` function:

```go
import (
	gi "github.com/seatgeek/go-instrument"
)

instruments := gi.DefaultConfig("default_app_name")
```

You can now use the this config struct as a middleware for any negroni-based server:

```go
app := negroni.New(instruments)

// or
app := newgroni.New(...)
app.Use(instruments)
```

The instruments will automatically wrap the middleware stack and deliver metrics to NewRelic.

### Getting the instruments during a request

It is possible to get a hold of the instruments while handling a request. You can do this as a
means to track segments of your code or use the `NoticeError()` function to track problems during
the runtime.

```go
func statusHandler(w http.ResponseWriter, r *http.Request) {
	instruments := gi.GetInstruments(r)
	
	err := riskyOperation()
	instruments.NoticeError(err)
}
```

### Tracking segments with timers

It is often useful to track the performance of certain important parts for your code. You can do so
by using the timer functions. For example:

```go
func findFriends(i gi.Instruments) {
	defer i.StartTimer("friendship", "findFriends").End()
}
```

The first argument is `category`, and it is used to group similar metrics together. The second argument
is the `name` of the metric, usually just the "action" you are performing.
