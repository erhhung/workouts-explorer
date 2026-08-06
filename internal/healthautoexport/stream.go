package healthautoexport

import (
	"encoding/json"
	"errors"
	"io"
)

type tokenStream struct {
	decoder *json.Decoder
	limits  Limits
}

func newTokenStream(r io.Reader, limits Limits) (*tokenStream, *io.LimitedReader) {
	limited := &io.LimitedReader{R: r, N: limits.MaxInputBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.UseNumber()
	return &tokenStream{decoder: decoder, limits: limits}, limited
}

func (s *tokenStream) token() (json.Token, error) {
	token, err := s.decoder.Token()
	if err != nil {
		return nil, sanitizedTokenError(err)
	}
	if value, ok := token.(string); ok && len(value) > s.limits.MaxStringBytes {
		return nil, parseError(ErrorStringLimit)
	}
	if value, ok := token.(json.Number); ok && len(value) > s.limits.MaxStringBytes {
		return nil, parseError(ErrorStringLimit)
	}
	return token, nil
}

func (s *tokenStream) finish() error {
	if _, err := s.decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return sanitizedTokenError(err)
	}
	return parseError(ErrorInvalidJSON)
}

func sanitizedTokenError(err error) error {
	var syntax *json.SyntaxError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntax) {
		return parseError(ErrorInvalidJSON)
	}
	return parseError(ErrorReadFailure)
}

func (s *tokenStream) objectKey(seen map[string]struct{}) (string, error) {
	token, err := s.token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", parseError(ErrorInvalidJSON)
	}
	if _, duplicate := seen[key]; duplicate {
		return "", parseError(ErrorDuplicateKey)
	}
	if len(seen) >= s.limits.MaxUnknownCollectionItems {
		return "", parseError(ErrorCollectionLimit)
	}
	seen[key] = struct{}{}
	return key, nil
}

func (s *tokenStream) skipValue() error {
	start := s.decoder.InputOffset()
	token, err := s.token()
	if err != nil {
		return err
	}
	return s.skipTokenValue(token, start, 0)
}

func (s *tokenStream) skipTokenValue(token json.Token, start int64, depth int) error {
	if s.decoder.InputOffset()-start > s.limits.MaxUnknownValueBytes {
		return parseError(ErrorUnknownValueLimit)
	}
	if err := s.skipTokenDepth(token, start, depth); err != nil {
		return err
	}
	if s.decoder.InputOffset()-start > s.limits.MaxUnknownValueBytes {
		return parseError(ErrorUnknownValueLimit)
	}
	return nil
}

func (s *tokenStream) skipTokenDepth(token json.Token, start int64, depth int) error {
	if depth > s.limits.MaxNestingDepth {
		return parseError(ErrorNestingLimit)
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for s.decoder.More() {
			if _, err := s.objectKey(seen); err != nil {
				return err
			}
			if s.decoder.InputOffset()-start > s.limits.MaxUnknownValueBytes {
				return parseError(ErrorUnknownValueLimit)
			}
			nested, err := s.token()
			if err != nil {
				return err
			}
			if err := s.skipTokenDepth(nested, start, depth+1); err != nil {
				return err
			}
		}
		return s.expectDelim('}')
	case '[':
		count := 0
		for s.decoder.More() {
			if count >= s.limits.MaxUnknownCollectionItems {
				return parseError(ErrorCollectionLimit)
			}
			count++
			nested, err := s.token()
			if err != nil {
				return err
			}
			if err := s.skipTokenDepth(nested, start, depth+1); err != nil {
				return err
			}
			if s.decoder.InputOffset()-start > s.limits.MaxUnknownValueBytes {
				return parseError(ErrorUnknownValueLimit)
			}
		}
		return s.expectDelim(']')
	default:
		return parseError(ErrorInvalidJSON)
	}
}

func (s *tokenStream) expectDelim(want json.Delim) error {
	token, err := s.token()
	if err != nil {
		return err
	}
	if token != want {
		return parseError(ErrorInvalidJSON)
	}
	return nil
}

func parseError(code ErrorCode) *ParseError {
	return &ParseError{Code: code, Workout: -1, RoutePoint: -1}
}
