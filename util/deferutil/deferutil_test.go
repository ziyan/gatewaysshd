package deferutil_test

import (
	"errors"
	"testing"

	"github.com/ziyan/gatewaysshd/util/deferutil"
)

type testResource struct {
	closeError error
}

func (self *testResource) Close() error {
	return self.closeError
}

//nolint:nonamedreturns
func testFunction(returnError, closeError error) (err error) {
	resource := &testResource{
		closeError: closeError,
	}
	defer deferutil.Run(resource.Close, &err)

	return returnError
}

var (
	ErrReturnError = errors.New("deferutil: test return error")
	ErrCloseError  = errors.New("deferutil: test close error")
)

var testCases = []struct {
	returnError, closeError error
}{
	{nil, nil},
	{nil, ErrCloseError},
	{ErrReturnError, nil},
	{ErrReturnError, ErrCloseError},
}

func TestDeferredRun(t *testing.T) {
	t.Parallel()

	for index, testCase := range testCases {
		expectedError := testCase.returnError
		if expectedError == nil {
			expectedError = testCase.closeError
		}
		err := testFunction(testCase.returnError, testCase.closeError)
		if !errors.Is(err, expectedError) {
			t.Fatalf("test case %d failed, return error %q, close error %q, expecting %q, got %q", index, testCase.returnError, testCase.closeError, expectedError, err)
		}
	}
}
