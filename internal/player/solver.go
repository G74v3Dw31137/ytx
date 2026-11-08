// Adapted from B/C's original-player Goja boundary; see THIRD_PARTY.md.
package player

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/dop251/goja"
)

var (
	ErrUnsupported    = errors.New("ytx: unsupported player JavaScript")
	ErrAmbiguous      = errors.New("ytx: ambiguous player function discovery")
	ErrPreparation    = errors.New("ytx: player preparation failed")
	ErrInitialization = errors.New("ytx: player runtime initialization failed")
	ErrExecution      = errors.New("ytx: player transformation failed")
)

const MaxSource = 8 << 20
const MaxValue = 16 << 10
const executionTimeout = 5 * time.Second
const adapters = `;var __extractSig=function(s){return __extractTransform(s,undefined).sig};var __extractN=function(n){return __extractTransform(undefined,n).n};`

// Prepare retains initialization in the original closure; unsupported startup fails explicitly.
func Prepare(source []byte) (string, error) {
	if len(source) == 0 || len(source) > MaxSource {
		return "", ErrUnsupported
	}
	return preparePlayer(string(source))
}

// CompileCode returns an immutable program, safe to use in independent runtimes.
func CompileCode(code string) (*goja.Program, error) {
	p, e := goja.Compile("player-solver.js", code+adapters, false)
	if e != nil {
		return nil, ErrPreparation
	}
	return p, nil
}

func Compile(ctx context.Context, source []byte) (*goja.Program, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	code, e := Prepare(source)
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	p, e := CompileCode(code)
	if ce := ctx.Err(); ce != nil {
		return nil, ce
	}
	return p, e
}

// Session owns a fresh runtime for exactly one public Resolve; it is never pooled.
type Session struct {
	vm     *goja.Runtime
	sig, n goja.Callable
}

func NewSession(ctx context.Context, p *goja.Program) (session *Session, err error) {
	defer func() {
		if recover() != nil {
			session = nil
			err = ErrInitialization
		}
	}()
	s := &Session{vm: goja.New()}
	s.vm.SetMaxCallStackSize(512)
	e := s.run(ctx, func() error {
		if _, e := s.vm.RunProgram(p); e != nil {
			return e
		}
		// Global reads can invoke JS accessors, so they share the initialization's
		// interrupt boundary rather than executing after its callback is disarmed.
		method := s.vm.Get("__extractMethodSource")
		if method == nil || method.ExportType() != reflect.TypeOf("") || !validMethodShape(method.String()) {
			return ErrUnsupported
		}
		var ok bool
		s.sig, ok = goja.AssertFunction(s.vm.Get("__extractSig"))
		if !ok {
			return ErrExecution
		}
		s.n, ok = goja.AssertFunction(s.vm.Get("__extractN"))
		if !ok {
			return ErrExecution
		}
		return nil
	})
	if e != nil {
		return nil, errors.Join(ErrInitialization, e)
	}
	return s, nil
}

// run joins the interrupt callback and clears the interrupt before returning.
// Time limits are cooperative; they do not provide a hard heap isolation boundary.
func (s *Session) run(ctx context.Context, fn func() error) (err error) {
	work, cancel := context.WithTimeout(ctx, executionTimeout)
	defer cancel()
	if e := work.Err(); e != nil {
		return e
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(work, func() { s.vm.Interrupt("canceled"); close(joined) })
	defer func() {
		if !stop() {
			<-joined
		}
		s.vm.ClearInterrupt()
		if recover() != nil {
			err = ErrExecution
		}
		if e := work.Err(); e != nil {
			err = e
		}
	}()
	if e := fn(); e != nil {
		if e == ErrUnsupported || e == ErrAmbiguous {
			return e
		}
		var exception *goja.Exception
		if errors.As(e, &exception) && exception.Value().ExportType() == reflect.TypeOf("") {
			switch exception.Value().String() {
			case "ytx:ambiguous":
				return ErrAmbiguous
			case "ytx:unsupported":
				return ErrUnsupported
			}
		}
		return ErrExecution
	}
	return nil
}

func (s *Session) apply(ctx context.Context, fn goja.Callable, input string) (out string, err error) {
	if len(input) == 0 || len(input) > MaxValue {
		return "", ErrExecution
	}
	err = s.run(ctx, func() error {
		v, e := fn(goja.Undefined(), s.vm.ToValue(input))
		if e != nil {
			return e
		}
		// Check primitive type before Export: exporting an object can traverse it.
		if v == nil || goja.IsNull(v) || goja.IsUndefined(v) || v.ExportType() != reflect.TypeOf("") {
			return ErrExecution
		}
		value := v.String()
		if value == "" || len(value) > MaxValue || strings.HasPrefix(value, "enhanced_except") {
			return ErrExecution
		}
		out = value
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}
func (s *Session) Signature(ctx context.Context, input string) (string, error) {
	return s.apply(ctx, s.sig, input)
}
func (s *Session) N(ctx context.Context, input string) (string, error) {
	return s.apply(ctx, s.n, input)
}
