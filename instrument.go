package instrument

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/codegangsta/negroni"
	"github.com/seatgeek/telemetria"
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
// Instruments. That is, the instance of the telemetria.Recorder
// configuration that will be used for tracking requests and custom segments.
//
// In general it is not a good idea to instantiate this yourself, you can use
// `DefaultConfig(string)` instead.
//
type InstrumentsConfig struct {
	// The recorder to be used for storing influxdb metrics
	statsRecorder telemetria.Recorder
}

// Segment is a sort of sub-transaction that is used to time blocks of code
type Segment interface {
	// Stops the timer and records the results
	End()
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

	// GetTransaction Get a current NewRelic transaction
	GetTransaction() *EmptyTransaction
}

type fullInstruments struct {
	influxdb telemetria.Recorder

	nrTransaction *EmptyTransaction

	txnName string

	config *InstrumentsConfig
}

// WithoutNewRelic indicates that no new relic middleware instrumentation
type WithoutNewRelic struct{}

// NoTransaction represents a mocked tracing transaction. Useful for development
// or during testing
type NoTransaction struct{}

// NoSegment means that we are not actually tracking the block of code, even though
// it was requested. This is useful for testing and for custom implementations
// of Instruments
type NoSegment struct{}

// End does nothing
func (n NoSegment) End() {}

// WithTransaction creates new Instruments associated with the passed transaction
// that can be used from tracking various aspects of the application as long as
// the transaction remains open
func (i *InstrumentsConfig) WithTransaction(name string, txn *EmptyTransaction) Instruments {
	return &fullInstruments{
		influxdb:      i.statsRecorder,
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
	txn := i.StartTransaction(r.URL.Path, rw, r)
	defer txn.End()

	txn.AddAttribute("query", r.URL.RawQuery)

	name := r.Method + " " + numberRegex.ReplaceAllString(r.URL.Path, "*")

	instruments := i.WithTransaction(name, txn)
	ctx := i.SetInstrumentsOnContext(r.Context(), instruments)
	r = r.WithContext(ctx)

	timer := instruments.StartNoTracingTimer("requests", name)

	res := negroni.NewResponseWriter(rw)
	next(res, r)

	timer.EndWithFields(FieldsList{"status": res.Status()})
}

// SetInstrumentsOnContext Set Instruments on a context value
func (i *InstrumentsConfig) SetInstrumentsOnContext(ctx context.Context, instruments Instruments) context.Context {
	return context.WithValue(ctx, instrumentsCtx{}, instruments)
}

func (i *InstrumentsConfig) StartTransaction(name string, w http.ResponseWriter, r *http.Request) *EmptyTransaction {
	return &EmptyTransaction{}
}

// GetInstruments Returns the Instruments struct that is attached to a request
// This struct can be used to time or trace segments of the request
func GetInstruments(r *http.Request) Instruments {
	return GetInstrumentsFromContext(r.Context())
}

// GetInstrumentsFromContext Returns the Instruments struct that is attached to a context
// This struct can be used to time or trace segments of the request
func GetInstrumentsFromContext(ctx context.Context) Instruments {
	return ctx.Value(instrumentsCtx{}).(Instruments)
}

// NoticeError just delegates the error tracking to the New Relic instance
func (i *fullInstruments) NoticeError(e error) {
	i.nrTransaction.NoticeError(e)
}

// WithOfflineTransaction Will start a new transaction with a similar name to the
// parent transaction. This method is mainly used to track code that is running
// in goroutines other than the one that spun the first transaction.
func (i *fullInstruments) WithOfflineTransaction(f func(Instruments)) {
	name := "(offline)" + i.txnName
	txn := i.config.StartTransaction(name, nil, nil)
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
		segment:     NoSegment{},
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
		segment:     NoSegment{},
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

	if len(fields) == 0 {
		fields = FieldsList{"count": 1}
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

func (i *fullInstruments) GetTransaction() *EmptyTransaction {
	return &EmptyTransaction{}
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
		nrTransaction: &EmptyTransaction{},
		txnName:       "test_txn",
		config: &InstrumentsConfig{
			statsRecorder: recorder,
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

func (n NoInstruments) GetTransaction() *EmptyTransaction {
	return &EmptyTransaction{}
}

type EmptyTransaction struct {
	http.ResponseWriter
}

func (e *EmptyTransaction) End() error {
	return nil
}

func (e *EmptyTransaction) Ignore() error {
	return nil
}

func (e *EmptyTransaction) SetName(name string) error {
	return nil
}

func (e *EmptyTransaction) NoticeError(err error) error {
	return nil
}

func (e *EmptyTransaction) AddAttribute(key string, value interface{}) error {
	return nil
}

type EmptyDistributedTracePayload struct{}

func (e *EmptyDistributedTracePayload) HTTPSafe() string {
	return ""
}
func (e *EmptyDistributedTracePayload) Text() string {
	return ""
}
