//go:build !linux

package wifiregion

import (
	"context"
	"errors"
)

func ApplyDriverCountry(context.Context, string, string) error {
	return errors.New("AIC8800 regulatory control requires Linux")
}

func ReadDriverCountry(context.Context, string) (string, error) {
	return "", errors.New("AIC8800 regulatory control requires Linux")
}
