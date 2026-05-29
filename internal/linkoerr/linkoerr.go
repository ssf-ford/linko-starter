package linkoerr

import (
	"errors"
	"log/slog"
)

type errWithAttr struct {
	error
	attrs []slog.Attr
}

func WithAttrs(err error, args ...any) error {
	return &errWithAttr{
		error: err,
		attrs: argsToAttrs(args...),
	}
}

func argsToAttrs(args ...any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args))
	for i := 0; i < len(args); {
		switch key := args[i].(type) {
		case slog.Attr:
			attrs = append(attrs, key)
			i++
		case string:
			if i+1 >= len(args) {
				attrs = append(attrs, slog.String("!BADKEY", key))
				i++
			} else {
				attrs = append(attrs, slog.Any(key, args[i+1]))
				i += 2
			}
		default:
			attrs = append(attrs, slog.Any("!BADKEY", key))
			i++
		}
	}
	return attrs
}

func (e *errWithAttr) Unwrap() error {
	return e.error
}

func (e *errWithAttr) Attrs() []slog.Attr {
	return e.attrs
}

type attrError interface {
	Attrs() []slog.Attr
}

func Attrs(err error) []slog.Attr {
	var attrs []slog.Attr
	for err != nil  {
		if ae, ok := err.(attrError); ok {
			attrs = append(attrs, ae.Attrs()...)
		}
		err = errors.Unwrap(err)
	}
	return attrs
}
