package instrument

import (
	"context"
	"fmt"
	"github.com/codegangsta/negroni"
	newrelic "github.com/newrelic/go-agent"
	"github.com/seatgeek/telemetria"
	"net/http"
	"regexp"
	"time"
)

var numberRegex = regexp.MustCompile(`[\d]+`)

type instrumentsCtx struct{}

// TagsList is the map of string to string containing the indexable extra
// attributes to store for timers and custom events. For example
//
//	instrument.TagsList{"model": "ticket", "type": "expensive"}
//
type TagsList map[string]string

// FieldsList is the map of string to interface{} containing the non-indexable
// values that should be stored along with a metric for a timer or a custom event.
// They are usually numeric and would not make sense to group by them.
//
//	instrument.FieldsList{"count": 5000, "elapsed": 0.334}
//
type FieldsList map[string]interface{}

// Category is a type alias for string. A Category name is used for tracking time
// and storing custom events. The category name is often used as the table name
// in influxdb. It is the more general idea of what is being stored.
type Category string

// InstrumentsConfig contains the configuration that will be used for creating the
// Instruments. That is, the instance of the telemetria.Recorder and the NewRelic
// configuration that will be used for tracking requests and custom segments.
//
// In general it is not a good idea to instantiate this yourself, you can use
// `DefaultConfig(string)` instead.
//
type InstrumentsConfig struct {
	// The recorder to be used for storing influxdb metrics
	StatsRecorder telemetria.Recorder

	// The NewRelic implementation to use for tracking segments
	Tracer NewRelic
}

// NewRelic encapsulates the single responsibility of creating a tracing transaction
// out of a server request.
type NewRelic interface {
	//Creates a new transaction out of a custom name and the arguments of a server request
	NewTransaction(string, http.ResponseWriter, *http.Request) Transaction
}

// Transaction encapsulates most of the idea of tracking information about a request.
// a Transaction is started at the beginning of each request and closed immediately after.
type Transaction interface {
	// Store additional information for this request as a key-value pair
	AddAttribute(string, string)

	// Starts a sort of sub-stransaction attached to this one. Segments are used
	// for timing blocks of code
	StartSegment(name string) Segment

	// Records that an error happened during this transaction
	NoticeError(e error)

	// Finishes the transaction and stores all the gathered data
	End()
}

// Segment is a sort of sub-transaction that is used to time blocks of code
type Segment interface {
	// Stops the timer and records the results
	End()
}

// NewRelicTransaction ins the implementation of Transaction using the NewRelic
// library.
type NewRelicTransaction struct {
	// The New Relic Transaction
	Txn newrelic.Transaction
}

// Instruments contains all the external facing methods that are relevant to
// measuring performance and tracking errors during a request.
type Instruments interface {
	// ServeHTTP wraps the `next` handler around a transaction and registers new Instruments
	// into the request context so that they can be used directly in each of the handlers
	// in the middleware stack
	ServeHTTP(http.ResponseWriter, *http.Request, http.HandlerFunc)

	// Reports the occurrance of an error
	NoticeError(e error)

	// Records a custom event. Default implementations will call this function to store
	// the results of timers.
	RecordEvent(Category, string, FieldsList, TagsList)

	// StartTimer returns a Timer that can be stoped at the end of an operation
	// the resulting elapsed time will be logged and recorded in influxdb
	//
	// Its intended use is:
	//
	//	defer instruments.StartTimer("handlers", "my_action").End()
	//
	StartTimer(Category, string) *Timer

	// Starts a new timers and gives it additional meta information to be stored
	// along with the timing result.
	StartTimerWithTags(Category, string, TagsList) *Timer

	// StartNoTracingTimer starts a timers that does not create a segment. This is
	// mainly used for tracking the top level request
	StartNoTracingTimer(Category, string) *Timer

	// WithResultTiming Times the passed function into error, panic, or success categories.
	WithResultTiming(Category, string, TagsList, func() error) error

	// WithOfflineTransaction will call the function with a new set of instruments that start a new
	// transaction and that can be used to trace code running offline. That is, after
	// the response has been sent to the user.o
	//
	// Proper implementations of this method will automatically end the transaction
	// and the end of the method
	WithOfflineTransaction(func(Instruments))
}

type fullInstruments struct {
	influxdb telemetria.Recorder

	nrTransaction Transaction

	txnName string

	config *InstrumentsConfig
}

// HasNewRelic contains the newrelicApp and indicates it should be used for instrumentation
type HasNewRelic struct {
	Application *newrelic.Application
}

// WithoutNewRelic indicates that no new relic middleware instrumentation
type WithoutNewRelic struct{}

// NoTransaction represents a mocked tracing transaction. Useful for development
// or during testing
type NoTransaction struct{}

// NewRelicSegment is the implementation of the Segment interface using New Relic
// segments
type NewRelicSegment struct {
	Segment newrelic.Segment
}

// NoSegment means that we are not actually tracking the block of code, even though
// it was requested. This is useful for testing and for custom implementations
// of Instruments
type NoSegment struct{}

// NewTransaction starts tracking the request from the client by giving it a custom
// name and both the response and request structs.
func (n HasNewRelic) NewTransaction(name string, rw http.ResponseWriter, r *http.Request) Transaction {
	app := *n.Application
	txn := app.StartTransaction(name, rw, r)

	if r.URL.Path == "/_status" {
		txn.Ignore()
	}

	return NewRelicTransaction{Txn: txn}
}

// AddAttribute stores the key-value attribute for the transaction directly with
// NewRelic without altering it.
func (t NewRelicTransaction) AddAttribute(name string, value string) {
	t.Txn.AddAttribute(name, value)
}

// End terminates the New Relic transaction
func (t NewRelicTransaction) End() {
	t.Txn.End()
}

// NoticeError uses the New Relic's underlying API to store the error associated
// with the current transaction
func (t NewRelicTransaction) NoticeError(e error) {
	t.Txn.NoticeError(e)
}

// StartSegment starts a New Relic plain Segment
func (t NewRelicTransaction) StartSegment(name string) Segment {
	return NewRelicSegment{Segment: newrelic.StartSegment(t.Txn, name)}
}

// End terminates the New Relic segment
func (s NewRelicSegment) End() {
	s.Segment.End()
}

// NewTransaction returns a mocked Transaction
func (n WithoutNewRelic) NewTransaction(name string, rw http.ResponseWriter, r *http.Request) Transaction {
	return NoTransaction{}
}

// End does nothing
func (n NoSegment) End() {}

// AddAttribute does nothing
func (t NoTransaction) AddAttribute(name string, value string) {}

// End does nothing
func (t NoTransaction) End() {}

// NoticeError does nothing
func (t NoTransaction) NoticeError(e error) {}

// StartSegment returns a mocked Segment
func (t NoTransaction) StartSegment(name string) Segment {
	return NoSegment{}
}

// WithTransaction creates new Instruments associated with the passed transaction
// that can be used from tracking various aspects of the application as long as
// the transaction remains open
func (i *InstrumentsConfig) WithTransaction(name string, txn Transaction) Instruments {
	return &fullInstruments{
		influxdb:      i.StatsRecorder,
		nrTransaction: txn,
		txnName:       name,
		config:        i,
	}
}

// ServeHTTP delegates the tracking of the request to the stored InstrumentsConfig
func (i *fullInstruments) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	i.config.ServeHTTP(rw, r, next)
}

// ServeHTTP wraps the `next` handler around a transaction and registers new Instruments
// into the request context so that they can be used directly in each of the handlers
// in the middleware stack
func (i *InstrumentsConfig) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	txn := i.Tracer.NewTransaction(r.URL.Path, rw, r)
	defer txn.End()

	txn.AddAttribute("query", r.URL.RawQuery)

	name := r.Method + " " + numberRegex.ReplaceAllString(r.URL.Path, "*")
	instruments := i.WithTransaction(name, txn)

	ctx := r.Context()
	r = r.WithContext(context.WithValue(ctx, instrumentsCtx{}, instruments))
	timer := instruments.StartNoTracingTimer("requests", name)

	res := negroni.NewResponseWriter(rw)
	next(res, r)

	timer.EndWithFields(FieldsList{"status": res.Status()})
}

// GetInstruments Returns the Intruments struct that is attached to a request
// This struct can be used to time or trace segments of the request
func GetInstruments(r *http.Request) Instruments {
	return r.Context().Value(instrumentsCtx{}).(Instruments)
}

// NoticeError just delegates the error tracking to the New Relic instance
func (i *fullInstruments) NoticeError(e error) {
	i.nrTransaction.NoticeError(e)
}

// WithOfflineTransaction Will start a new transaction with a similar name to the
// parent transaction. This method is mainly used to track code that is runinng
// in goroutines other than the one that spun the first transaction.
func (i *fullInstruments) WithOfflineTransaction(f func(Instruments)) {
	name := "(offline)" + i.txnName
	txn := i.config.Tracer.NewTransaction(name, nil, nil)
	defer txn.End()

	instruments := i.config.WithTransaction(name, txn)
	f(instruments)
}

//
// Timing related methods
//

// Timer is used for tracking the elapsed time of an operation
type Timer struct {
	// The name as should be stored in the timers table
	category    Category
	name        string
	start       time.Time
	fields      FieldsList
	tags        TagsList
	instruments Instruments
	segment     Segment
}

// StartTimer returns a Timer that can be stoped at the end of an operation
// the resulting elapsed time will be logged and recorded in influxdb
//
// Its intended use is:
//
//	defer instruments.StartTimer("handlers", "my_action").End()
//
func (i *fullInstruments) StartTimer(c Category, name string) *Timer {
	return &Timer{
		category:    c,
		name:        name,
		start:       time.Now(),
		instruments: i,
		tags:        TagsList{},
		fields:      FieldsList{},
		segment:     i.nrTransaction.StartSegment(string(c) + "::" + name),
	}
}

// StartTimerWithTags starts a new timer after first assotiating some tags to it.
// The tags will be stored as indexable columns in influxdb
func (i *fullInstruments) StartTimerWithTags(c Category, name string, tags TagsList) *Timer {
	segmentName := string(c)
	for k, v := range tags {
		segmentName = fmt.Sprintf("%s :: (%s=%s) ", segmentName, k, v)
	}
	segmentName = segmentName + " :: " + name

	return &Timer{
		category:    c,
		name:        name,
		start:       time.Now(),
		instruments: i,
		tags:        tags,
		segment:     i.nrTransaction.StartSegment(segmentName),
	}
}

// StartNoTracingTimer starts a timers that does not create a segment. This is
// mainly used for tracking the top level request
func (i *fullInstruments) StartNoTracingTimer(c Category, name string) *Timer {
	return &Timer{
		category:    c,
		name:        name,
		start:       time.Now(),
		instruments: i,
		tags:        TagsList{},
		fields:      FieldsList{},
		segment:     NoSegment{},
	}
}

// End calculates the elapsed time since the timer was started and logs the
// information in influxdb
//
// Its intended use is `defer instruments.StartTimer("my_action").End()`
func (t *Timer) End() {
	defer t.segment.End()
	took := time.Since(t.start)

	newTagList := copyTags(t.tags)
	newFieldList := copyFields(t.fields)
	newFieldList["elapsed"] = took.Seconds()
	newTagList["action"] = t.name

	t.instruments.RecordEvent(t.category, t.name, newFieldList, newTagList)
}

// EndWithFields stops the timer and saves additional data calculated during the
// time it was run.
//
//	timer := intruments.StartTimer("events", "pdf_download")
//	...
//	timer.EndWithFields(instrument.FieldsList{"total_bytes": fileSize})
//
func (t *Timer) EndWithFields(fields FieldsList) {
	t.fields = fields
	t.End()
}

// WithResultTiming times the passed function and records any error that comes
// out of it
func (i *fullInstruments) WithResultTiming(c Category, name string, tags TagsList,
	f func() error) (err error) {

	if tags == nil {
		tags = TagsList{}
	}

	timer := i.StartTimerWithTags(c, name, tags)

	defer func() {
		panicValue := recover()

		if panicValue != nil {
			// Set error to ensure an error is returned on panic. We don't
			// want to silently recover from a panic
			err = fmt.Errorf("Panic recovered in WithResultTiming: %v", panicValue)
			i.NoticeError(err)
			timer.EndWithFields(FieldsList{"status": "panicked"})
			return
		}

		if err != nil {
			timer.EndWithFields(FieldsList{"status": "errored"})
			i.NoticeError(err)
			return
		}

		timer.EndWithFields(FieldsList{"status": "succeeded"})
	}()

	err = f()

	return err
}

//
// Custom event emitting functions
//

// RecordEvent sends the event only to influxDB
func (i *fullInstruments) RecordEvent(c Category, name string, fields FieldsList, tags TagsList) {
	if tags == nil {
		tags = TagsList{}
	}

	// This prevents race conditions as maps are passed by reference
	newTagList := copyTags(tags)
	newTagList["event"] = name

	i.influxdb.WriteOne(telemetria.Metric{
		Name:   string(c),
		Fields: fields,
		Tags:   newTagList,
	})
}

func copyFields(fields FieldsList) FieldsList {
	newList := FieldsList{}
	for k, v := range fields {
		newList[k] = v
	}

	return newList
}

func copyTags(tags TagsList) TagsList {
	newList := TagsList{}
	for k, v := range tags {
		newList[k] = v
	}

	return newList
}

//
// Mocked instruments
//

// NewMockedInstruments  will return Instruments that can be safely used during
// testing
func NewMockedInstruments() Instruments {
	recorder, _ := telemetria.NewRecorder("udp://localhost:13378")

	return &fullInstruments{
		influxdb:      recorder,
		nrTransaction: NoTransaction{},
		txnName:       "test_txn",
		config: &InstrumentsConfig{
			StatsRecorder: recorder,
			Tracer:        WithoutNewRelic{},
		},
	}
}

// NoInstruments is the mocked version of Instruments. It can be used during testing
// or whenever a function is expecting Instruments to be passed but none can be
// obtained.
type NoInstruments struct {
}

// ServeHTTP just calls the next handler
func (n NoInstruments) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	next(rw, r)
}

// NoticeError does nothing
func (n NoInstruments) NoticeError(e error) {}

// RecordEvent does nothing
func (n NoInstruments) RecordEvent(c Category, name string, f FieldsList, t TagsList) {}

//StartTimer return a mocked timer
func (n NoInstruments) StartTimer(c Category, name string) *Timer {
	return n.StartTimerWithTags(c, name, TagsList{})
}

// StartNoTracingTimer retunds a mocked timer
func (n NoInstruments) StartNoTracingTimer(c Category, name string) *Timer {
	return n.StartTimerWithTags(c, name, TagsList{})
}

// StartTimerWithTags returns a mocked timer
func (n NoInstruments) StartTimerWithTags(c Category, name string, tags TagsList) *Timer {
	return &Timer{
		category:    c,
		name:        name,
		start:       time.Now(),
		instruments: n,
		tags:        tags,
		fields:      FieldsList{},
		segment:     NoSegment{},
	}
}

// WithResultTiming just calls the passed function without actually timing it
func (n NoInstruments) WithResultTiming(c Category, name string, t TagsList, f func() error) error {
	return f()
}

// WithOfflineTransaction will call the passed function with NoInstruments
func (n NoInstruments) WithOfflineTransaction(f func(Instruments)) {
	f(n)
}
