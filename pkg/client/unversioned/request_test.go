/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package unversioned

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/kubernetes/pkg/api"
	apierrors "k8s.io/kubernetes/pkg/api/errors"
	"k8s.io/kubernetes/pkg/api/testapi"
	"k8s.io/kubernetes/pkg/api/unversioned"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/httpstream"
	"k8s.io/kubernetes/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/watch"
	watchjson "k8s.io/kubernetes/pkg/watch/json"
)

func TestRequestWithErrorWontChange(t *testing.T) {
	original := Request{
		err:        errors.New("test"),
		apiVersion: testapi.Default.Version(),
	}
	r := original
	changed := r.Param("foo", "bar").
		LabelsSelectorParam(labels.Set{"a": "b"}.AsSelector()).
		UintParam("uint", 1).
		AbsPath("/abs").
		Prefix("test").
		Suffix("testing").
		Namespace("new").
		Resource("foos").
		Name("bars").
		Body("foo").
		Timeout(time.Millisecond)
	if changed != &r {
		t.Errorf("returned request should point to the same object")
	}
	if !reflect.DeepEqual(changed, &original) {
		t.Errorf("expected %#v, got %#v", &original, changed)
	}
}

func TestRequestPreservesBaseTrailingSlash(t *testing.T) {
	r := &Request{baseURL: &url.URL{}, path: "/path/"}
	if s := r.URL().String(); s != "/path/" {
		t.Errorf("trailing slash should be preserved: %s", s)
	}
}

func TestRequestAbsPathPreservesTrailingSlash(t *testing.T) {
	r := (&Request{baseURL: &url.URL{}}).AbsPath("/foo/")
	if s := r.URL().String(); s != "/foo/" {
		t.Errorf("trailing slash should be preserved: %s", s)
	}

	r = (&Request{baseURL: &url.URL{}}).AbsPath("/foo/")
	if s := r.URL().String(); s != "/foo/" {
		t.Errorf("trailing slash should be preserved: %s", s)
	}
}

func TestRequestAbsPathJoins(t *testing.T) {
	r := (&Request{baseURL: &url.URL{}}).AbsPath("foo/bar", "baz")
	if s := r.URL().String(); s != "foo/bar/baz" {
		t.Errorf("trailing slash should be preserved: %s", s)
	}
}

func TestRequestSetsNamespace(t *testing.T) {
	r := (&Request{
		baseURL: &url.URL{
			Path: "/",
		},
	}).Namespace("foo")
	if r.namespace == "" {
		t.Errorf("namespace should be set: %#v", r)
	}

	if s := r.URL().String(); s != "namespaces/foo" {
		t.Errorf("namespace should be in path: %s", s)
	}
}

func TestRequestOrdersNamespaceInPath(t *testing.T) {
	r := (&Request{
		baseURL: &url.URL{},
		path:    "/test/",
	}).Name("bar").Resource("baz").Namespace("foo")
	if s := r.URL().String(); s != "/test/namespaces/foo/baz/bar" {
		t.Errorf("namespace should be in order in path: %s", s)
	}
}

func TestRequestOrdersSubResource(t *testing.T) {
	r := (&Request{
		baseURL: &url.URL{},
		path:    "/test/",
	}).Name("bar").Resource("baz").Namespace("foo").Suffix("test").SubResource("a", "b")
	if s := r.URL().String(); s != "/test/namespaces/foo/baz/bar/a/b/test" {
		t.Errorf("namespace should be in order in path: %s", s)
	}
}

func TestRequestSetTwiceError(t *testing.T) {
	if (&Request{}).Name("bar").Name("baz").err == nil {
		t.Errorf("setting name twice should result in error")
	}
	if (&Request{}).Namespace("bar").Namespace("baz").err == nil {
		t.Errorf("setting namespace twice should result in error")
	}
	if (&Request{}).Resource("bar").Resource("baz").err == nil {
		t.Errorf("setting resource twice should result in error")
	}
	if (&Request{}).SubResource("bar").SubResource("baz").err == nil {
		t.Errorf("setting subresource twice should result in error")
	}
}

func TestInvalidSegments(t *testing.T) {
	invalidSegments := []string{".", "..", "test/segment", "test%2bsegment"}
	setters := map[string]func(string, *Request){
		"namespace":   func(s string, r *Request) { r.Namespace(s) },
		"resource":    func(s string, r *Request) { r.Resource(s) },
		"name":        func(s string, r *Request) { r.Name(s) },
		"subresource": func(s string, r *Request) { r.SubResource(s) },
	}
	for _, invalidSegment := range invalidSegments {
		for setterName, setter := range setters {
			r := &Request{}
			setter(invalidSegment, r)
			if r.err == nil {
				t.Errorf("%s: %s: expected error, got none", setterName, invalidSegment)
			}
		}
	}
}

func TestRequestParam(t *testing.T) {
	r := (&Request{}).Param("foo", "a")
	if !reflect.DeepEqual(r.params, url.Values{"foo": []string{"a"}}) {
		t.Errorf("should have set a param: %#v", r)
	}

	r.Param("bar", "1")
	r.Param("bar", "2")
	if !reflect.DeepEqual(r.params, url.Values{"foo": []string{"a"}, "bar": []string{"1", "2"}}) {
		t.Errorf("should have set a param: %#v", r)
	}
}

func TestRequestVersionedParams(t *testing.T) {
	r := (&Request{apiVersion: "v1"}).Param("foo", "a")
	if !reflect.DeepEqual(r.params, url.Values{"foo": []string{"a"}}) {
		t.Errorf("should have set a param: %#v", r)
	}
	r.VersionedParams(&api.PodLogOptions{Follow: true, Container: "bar"}, api.Scheme)

	if !reflect.DeepEqual(r.params, url.Values{
		"foo":       []string{"a"},
		"container": []string{"bar"},
		"follow":    []string{"true"},
	}) {
		t.Errorf("should have set a param: %#v", r)
	}
}

func TestRequestVersionedParamsFromListOptions(t *testing.T) {
	r := &Request{apiVersion: "v1"}
	r.VersionedParams(&unversioned.ListOptions{ResourceVersion: "1"}, api.Scheme)
	if !reflect.DeepEqual(r.params, url.Values{
		"resourceVersion": []string{"1"},
	}) {
		t.Errorf("should have set a param: %#v", r)
	}

	var timeout int64 = 10
	r.VersionedParams(&unversioned.ListOptions{ResourceVersion: "2", TimeoutSeconds: &timeout}, api.Scheme)
	if !reflect.DeepEqual(r.params, url.Values{
		"resourceVersion": []string{"1", "2"},
		"timeoutSeconds":  []string{"10"},
	}) {
		t.Errorf("should have set a param: %#v", r)
	}
}

func TestRequestURI(t *testing.T) {
	r := (&Request{}).Param("foo", "a")
	r.Prefix("other")
	r.RequestURI("/test?foo=b&a=b&c=1&c=2")
	if r.path != "/test" {
		t.Errorf("path is wrong: %#v", r)
	}
	if !reflect.DeepEqual(r.params, url.Values{"a": []string{"b"}, "foo": []string{"b"}, "c": []string{"1", "2"}}) {
		t.Errorf("should have set a param: %#v", r)
	}
}

type NotAnAPIObject struct{}

func (NotAnAPIObject) IsAnAPIObject() {}

func TestRequestBody(t *testing.T) {
	// test unknown type
	r := (&Request{}).Body([]string{"test"})
	if r.err == nil || r.body != nil {
		t.Errorf("should have set err and left body nil: %#v", r)
	}

	// test error set when failing to read file
	f, err := ioutil.TempFile("", "test")
	if err != nil {
		t.Fatalf("unable to create temp file")
	}
	os.Remove(f.Name())
	r = (&Request{}).Body(f.Name())
	if r.err == nil || r.body != nil {
		t.Errorf("should have set err and left body nil: %#v", r)
	}

	// test unencodable api object
	r = (&Request{codec: testapi.Default.Codec()}).Body(&NotAnAPIObject{})
	if r.err == nil || r.body != nil {
		t.Errorf("should have set err and left body nil: %#v", r)
	}
}

func TestResultIntoWithErrReturnsErr(t *testing.T) {
	res := Result{err: errors.New("test")}
	if err := res.Into(&api.Pod{}); err != res.err {
		t.Errorf("should have returned exact error from result")
	}
}

func TestURLTemplate(t *testing.T) {
	uri, _ := url.Parse("http://localhost")
	r := NewRequest(nil, "POST", uri, "test", nil)
	r.Prefix("pre1").Resource("r1").Namespace("ns").Name("nm").Param("p0", "v0")
	full := r.URL()
	if full.String() != "http://localhost/pre1/namespaces/ns/r1/nm?p0=v0" {
		t.Errorf("unexpected initial URL: %s", full)
	}
	actual := r.finalURLTemplate()
	expected := "http://localhost/pre1/namespaces/%7Bnamespace%7D/r1/%7Bname%7D?p0=%7Bvalue%7D"
	if actual != expected {
		t.Errorf("unexpected URL template: %s %s", actual, expected)
	}
	if r.URL().String() != full.String() {
		t.Errorf("creating URL template changed request: %s -> %s", full.String(), r.URL().String())
	}
}

func TestTransformResponse(t *testing.T) {
	invalid := []byte("aaaaa")
	uri, _ := url.Parse("http://localhost")
	testCases := []struct {
		Response *http.Response
		Data     []byte
		Created  bool
		Error    bool
		ErrFn    func(err error) bool
	}{
		{Response: &http.Response{StatusCode: 200}, Data: []byte{}},
		{Response: &http.Response{StatusCode: 201}, Data: []byte{}, Created: true},
		{Response: &http.Response{StatusCode: 199}, Error: true},
		{Response: &http.Response{StatusCode: 500}, Error: true},
		{Response: &http.Response{StatusCode: 422}, Error: true},
		{Response: &http.Response{StatusCode: 409}, Error: true},
		{Response: &http.Response{StatusCode: 404}, Error: true},
		{Response: &http.Response{StatusCode: 401}, Error: true},
		{
			Response: &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       ioutil.NopCloser(bytes.NewReader(invalid)),
			},
			Error: true,
			ErrFn: func(err error) bool {
				return err.Error() != "aaaaa" && apierrors.IsUnauthorized(err)
			},
		},
		{
			Response: &http.Response{
				StatusCode: 401,
				Header:     http.Header{"Content-Type": []string{"text/any"}},
				Body:       ioutil.NopCloser(bytes.NewReader(invalid)),
			},
			Error: true,
			ErrFn: func(err error) bool {
				return strings.Contains(err.Error(), "server has asked for the client to provide") && apierrors.IsUnauthorized(err)
			},
		},
		{Response: &http.Response{StatusCode: 403}, Error: true},
		{Response: &http.Response{StatusCode: 200, Body: ioutil.NopCloser(bytes.NewReader(invalid))}, Data: invalid},
		{Response: &http.Response{StatusCode: 200, Body: ioutil.NopCloser(bytes.NewReader(invalid))}, Data: invalid},
	}
	for i, test := range testCases {
		r := NewRequest(nil, "", uri, testapi.Default.Version(), testapi.Default.Codec())
		if test.Response.Body == nil {
			test.Response.Body = ioutil.NopCloser(bytes.NewReader([]byte{}))
		}
		result := r.transformResponse(test.Response, &http.Request{})
		response, created, err := result.body, result.statusCode == http.StatusCreated, result.err
		hasErr := err != nil
		if hasErr != test.Error {
			t.Errorf("%d: unexpected error: %t %v", i, test.Error, err)
		} else if hasErr && test.Response.StatusCode > 399 {
			status, ok := err.(APIStatus)
			if !ok {
				t.Errorf("%d: response should have been transformable into APIStatus: %v", i, err)
				continue
			}
			if int(status.Status().Code) != test.Response.StatusCode {
				t.Errorf("%d: status code did not match response: %#v", i, status.Status())
			}
		}
		if test.ErrFn != nil && !test.ErrFn(err) {
			t.Errorf("%d: error function did not match: %v", i, err)
		}
		if !(test.Data == nil && response == nil) && !api.Semantic.DeepDerivative(test.Data, response) {
			t.Errorf("%d: unexpected response: %#v %#v", i, test.Data, response)
		}
		if test.Created != created {
			t.Errorf("%d: expected created %t, got %t", i, test.Created, created)
		}
	}
}

func TestTransformUnstructuredError(t *testing.T) {
	testCases := []struct {
		Req *http.Request
		Res *http.Response

		Resource string
		Name     string

		ErrFn func(error) bool
	}{
		{
			Resource: "foo",
			Name:     "bar",
			Req: &http.Request{
				Method: "POST",
			},
			Res: &http.Response{
				StatusCode: http.StatusConflict,
				Body:       ioutil.NopCloser(bytes.NewReader(nil)),
			},
			ErrFn: apierrors.IsAlreadyExists,
		},
		{
			Resource: "foo",
			Name:     "bar",
			Req: &http.Request{
				Method: "PUT",
			},
			Res: &http.Response{
				StatusCode: http.StatusConflict,
				Body:       ioutil.NopCloser(bytes.NewReader(nil)),
			},
			ErrFn: apierrors.IsConflict,
		},
		{
			Resource: "foo",
			Name:     "bar",
			Req:      &http.Request{},
			Res: &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       ioutil.NopCloser(bytes.NewReader(nil)),
			},
			ErrFn: apierrors.IsNotFound,
		},
		{
			Req: &http.Request{},
			Res: &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       ioutil.NopCloser(bytes.NewReader(nil)),
			},
			ErrFn: apierrors.IsBadRequest,
		},
	}

	for _, testCase := range testCases {
		r := &Request{
			codec:        testapi.Default.Codec(),
			resourceName: testCase.Name,
			resource:     testCase.Resource,
		}
		result := r.transformResponse(testCase.Res, testCase.Req)
		err := result.err
		if !testCase.ErrFn(err) {
			t.Errorf("unexpected error: %v", err)
			continue
		}
		if len(testCase.Name) != 0 && !strings.Contains(err.Error(), testCase.Name) {
			t.Errorf("unexpected error string: %s", err)
		}
		if len(testCase.Resource) != 0 && !strings.Contains(err.Error(), testCase.Resource) {
			t.Errorf("unexpected error string: %s", err)
		}
	}
}

type clientFunc func(req *http.Request) (*http.Response, error)

func (f clientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRequestWatch(t *testing.T) {
	testCases := []struct {
		Request *Request
		Err     bool
		ErrFn   func(error) bool
		Empty   bool
	}{
		{
			Request: &Request{err: errors.New("bail")},
			Err:     true,
		},
		{
			Request: &Request{baseURL: &url.URL{}, path: "%"},
			Err:     true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("err")
				}),
				baseURL: &url.URL{},
			},
			Err: true,
		},
		{
			Request: &Request{
				codec: testapi.Default.Codec(),
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusForbidden}, nil
				}),
				baseURL: &url.URL{},
			},
			Err: true,
			ErrFn: func(err error) bool {
				return apierrors.IsForbidden(err)
			},
		},
		{
			Request: &Request{
				codec: testapi.Default.Codec(),
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusUnauthorized}, nil
				}),
				baseURL: &url.URL{},
			},
			Err: true,
			ErrFn: func(err error) bool {
				return apierrors.IsUnauthorized(err)
			},
		},
		{
			Request: &Request{
				codec: testapi.Default.Codec(),
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body: ioutil.NopCloser(bytes.NewReader([]byte(runtime.EncodeOrDie(testapi.Default.Codec(), &unversioned.Status{
							Status: unversioned.StatusFailure,
							Reason: unversioned.StatusReasonUnauthorized,
						})))),
					}, nil
				}),
				baseURL: &url.URL{},
			},
			Err: true,
			ErrFn: func(err error) bool {
				return apierrors.IsUnauthorized(err)
			},
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, io.EOF
				}),
				baseURL: &url.URL{},
			},
			Empty: true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, &url.Error{Err: io.EOF}
				}),
				baseURL: &url.URL{},
			},
			Empty: true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("http: can't write HTTP request on broken connection")
				}),
				baseURL: &url.URL{},
			},
			Empty: true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("foo: connection reset by peer")
				}),
				baseURL: &url.URL{},
			},
			Empty: true,
		},
	}
	for i, testCase := range testCases {
		watch, err := testCase.Request.Watch()
		hasErr := err != nil
		if hasErr != testCase.Err {
			t.Errorf("%d: expected %t, got %t: %v", i, testCase.Err, hasErr, err)
			continue
		}
		if testCase.ErrFn != nil && !testCase.ErrFn(err) {
			t.Errorf("%d: error not valid: %v", i, err)
		}
		if hasErr && watch != nil {
			t.Errorf("%d: watch should be nil when error is returned", i)
			continue
		}
		if testCase.Empty {
			_, ok := <-watch.ResultChan()
			if ok {
				t.Errorf("%d: expected the watch to be empty: %#v", i, watch)
			}
		}
	}
}

func TestRequestStream(t *testing.T) {
	testCases := []struct {
		Request *Request
		Err     bool
	}{
		{
			Request: &Request{err: errors.New("bail")},
			Err:     true,
		},
		{
			Request: &Request{baseURL: &url.URL{}, path: "%"},
			Err:     true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("err")
				}),
				baseURL: &url.URL{},
			},
			Err: true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body: ioutil.NopCloser(bytes.NewReader([]byte(runtime.EncodeOrDie(testapi.Default.Codec(), &unversioned.Status{
							Status: unversioned.StatusFailure,
							Reason: unversioned.StatusReasonUnauthorized,
						})))),
					}, nil
				}),
				codec:   testapi.Default.Codec(),
				baseURL: &url.URL{},
			},
			Err: true,
		},
	}
	for i, testCase := range testCases {
		body, err := testCase.Request.Stream()
		hasErr := err != nil
		if hasErr != testCase.Err {
			t.Errorf("%d: expected %t, got %t: %v", i, testCase.Err, hasErr, err)
		}
		if hasErr && body != nil {
			t.Errorf("%d: body should be nil when error is returned", i)
		}
	}
}

type fakeUpgradeConnection struct{}

func (c *fakeUpgradeConnection) CreateStream(headers http.Header) (httpstream.Stream, error) {
	return nil, nil
}
func (c *fakeUpgradeConnection) Close() error {
	return nil
}
func (c *fakeUpgradeConnection) CloseChan() <-chan bool {
	return make(chan bool)
}
func (c *fakeUpgradeConnection) SetIdleTimeout(timeout time.Duration) {
}

type fakeUpgradeRoundTripper struct {
	req  *http.Request
	conn httpstream.Connection
}

func (f *fakeUpgradeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.req = req
	b := []byte{}
	body := ioutil.NopCloser(bytes.NewReader(b))
	resp := &http.Response{
		StatusCode: 101,
		Body:       body,
	}
	return resp, nil
}

func (f *fakeUpgradeRoundTripper) NewConnection(resp *http.Response) (httpstream.Connection, error) {
	return f.conn, nil
}

func TestRequestDo(t *testing.T) {
	testCases := []struct {
		Request *Request
		Err     bool
	}{
		{
			Request: &Request{err: errors.New("bail")},
			Err:     true,
		},
		{
			Request: &Request{baseURL: &url.URL{}, path: "%"},
			Err:     true,
		},
		{
			Request: &Request{
				client: clientFunc(func(req *http.Request) (*http.Response, error) {
					return nil, errors.New("err")
				}),
				baseURL: &url.URL{},
			},
			Err: true,
		},
	}
	for i, testCase := range testCases {
		body, err := testCase.Request.Do().Raw()
		hasErr := err != nil
		if hasErr != testCase.Err {
			t.Errorf("%d: expected %t, got %t: %v", i, testCase.Err, hasErr, err)
		}
		if hasErr && body != nil {
			t.Errorf("%d: body should be nil when error is returned", i)
		}
	}
}

func TestDoRequestNewWay(t *testing.T) {
	reqBody := "request body"
	expectedObj := &api.Service{Spec: api.ServiceSpec{Ports: []api.ServicePort{{
		Protocol:   "TCP",
		Port:       12345,
		TargetPort: intstr.FromInt(12345),
	}}}}
	expectedBody, _ := testapi.Default.Codec().Encode(expectedObj)
	fakeHandler := util.FakeHandler{
		StatusCode:   200,
		ResponseBody: string(expectedBody),
		T:            t,
	}
	testServer := httptest.NewServer(&fakeHandler)
	defer testServer.Close()
	c := testRESTClient(t, testServer)
	obj, err := c.Verb("POST").
		Prefix("foo", "bar").
		Suffix("baz").
		Timeout(time.Second).
		Body([]byte(reqBody)).
		Do().Get()
	if err != nil {
		t.Errorf("Unexpected error: %v %#v", err, err)
		return
	}
	if obj == nil {
		t.Error("nil obj")
	} else if !api.Semantic.DeepDerivative(expectedObj, obj) {
		t.Errorf("Expected: %#v, got %#v", expectedObj, obj)
	}
	requestURL := testapi.Default.ResourcePathWithPrefix("foo/bar", "", "", "baz")
	requestURL += "?timeout=1s"
	fakeHandler.ValidateRequest(t, requestURL, "POST", &reqBody)
}

func TestCheckRetryClosesBody(t *testing.T) {
	count := 0
	ch := make(chan struct{})
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count++
		t.Logf("attempt %d", count)
		if count >= 5 {
			w.WriteHeader(http.StatusOK)
			close(ch)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(apierrors.StatusTooManyRequests)
	}))
	defer testServer.Close()

	c := testRESTClient(t, testServer)
	_, err := c.Verb("POST").
		Prefix("foo", "bar").
		Suffix("baz").
		Timeout(time.Second).
		Body([]byte(strings.Repeat("abcd", 1000))).
		DoRaw()
	if err != nil {
		t.Fatalf("Unexpected error: %v %#v", err, err)
	}
	<-ch
	if count != 5 {
		t.Errorf("unexpected retries: %d", count)
	}
}

func TestCheckRetryHandles429And5xx(t *testing.T) {
	count := 0
	ch := make(chan struct{})
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Logf("attempt %d", count)
		if count >= 4 {
			w.WriteHeader(http.StatusOK)
			close(ch)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader([]int{apierrors.StatusTooManyRequests, 500, 501, 504}[count])
		count++
	}))
	defer testServer.Close()

	c := testRESTClient(t, testServer)
	_, err := c.Verb("POST").
		Prefix("foo", "bar").
		Suffix("baz").
		Timeout(time.Second).
		Body([]byte(strings.Repeat("abcd", 1000))).
		DoRaw()
	if err != nil {
		t.Fatalf("Unexpected error: %v %#v", err, err)
	}
	<-ch
	if count != 4 {
		t.Errorf("unexpected retries: %d", count)
	}
}

func BenchmarkCheckRetryClosesBody(b *testing.B) {
	count := 0
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count++
		if count%3 == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(apierrors.StatusTooManyRequests)
	}))
	defer testServer.Close()

	c := testRESTClient(b, testServer)
	r := c.Verb("POST").
		Prefix("foo", "bar").
		Suffix("baz").
		Timeout(time.Second).
		Body([]byte(strings.Repeat("abcd", 1000)))

	for i := 0; i < b.N; i++ {
		if _, err := r.DoRaw(); err != nil {
			b.Fatalf("Unexpected error: %v %#v", err, err)
		}
	}
}

func TestDoRequestNewWayReader(t *testing.T) {
	reqObj := &api.Pod{ObjectMeta: api.ObjectMeta{Name: "foo"}}
	reqBodyExpected, _ := testapi.Default.Codec().Encode(reqObj)
	expectedObj := &api.Service{Spec: api.ServiceSpec{Ports: []api.ServicePort{{
		Protocol:   "TCP",
		Port:       12345,
		TargetPort: intstr.FromInt(12345),
	}}}}
	expectedBody, _ := testapi.Default.Codec().Encode(expectedObj)
	fakeHandler := util.FakeHandler{
		StatusCode:   200,
		ResponseBody: string(expectedBody),
		T:            t,
	}
	testServer := httptest.NewServer(&fakeHandler)
	defer testServer.Close()
	c := testRESTClient(t, testServer)
	obj, err := c.Verb("POST").
		Resource("bar").
		Name("baz").
		Prefix("foo").
		LabelsSelectorParam(labels.Set{"name": "foo"}.AsSelector()).
		Timeout(time.Second).
		Body(bytes.NewBuffer(reqBodyExpected)).
		Do().Get()
	if err != nil {
		t.Errorf("Unexpected error: %v %#v", err, err)
		return
	}
	if obj == nil {
		t.Error("nil obj")
	} else if !api.Semantic.DeepDerivative(expectedObj, obj) {
		t.Errorf("Expected: %#v, got %#v", expectedObj, obj)
	}
	tmpStr := string(reqBodyExpected)
	requestURL := testapi.Default.ResourcePathWithPrefix("foo", "bar", "", "baz")
	requestURL += "?" + unversioned.LabelSelectorQueryParam(testapi.Default.Version()) + "=name%3Dfoo&timeout=1s"
	fakeHandler.ValidateRequest(t, requestURL, "POST", &tmpStr)
}

func TestDoRequestNewWayObj(t *testing.T) {
	reqObj := &api.Pod{ObjectMeta: api.ObjectMeta{Name: "foo"}}
	reqBodyExpected, _ := testapi.Default.Codec().Encode(reqObj)
	expectedObj := &api.Service{Spec: api.ServiceSpec{Ports: []api.ServicePort{{
		Protocol:   "TCP",
		Port:       12345,
		TargetPort: intstr.FromInt(12345),
	}}}}
	expectedBody, _ := testapi.Default.Codec().Encode(expectedObj)
	fakeHandler := util.FakeHandler{
		StatusCode:   200,
		ResponseBody: string(expectedBody),
		T:            t,
	}
	testServer := httptest.NewServer(&fakeHandler)
	defer testServer.Close()
	c := testRESTClient(t, testServer)
	obj, err := c.Verb("POST").
		Suffix("baz").
		Name("bar").
		Resource("foo").
		LabelsSelectorParam(labels.Set{"name": "foo"}.AsSelector()).
		Timeout(time.Second).
		Body(reqObj).
		Do().Get()
	if err != nil {
		t.Errorf("Unexpected error: %v %#v", err, err)
		return
	}
	if obj == nil {
		t.Error("nil obj")
	} else if !api.Semantic.DeepDerivative(expectedObj, obj) {
		t.Errorf("Expected: %#v, got %#v", expectedObj, obj)
	}
	tmpStr := string(reqBodyExpected)
	requestURL := testapi.Default.ResourcePath("foo", "", "bar/baz")
	requestURL += "?" + unversioned.LabelSelectorQueryParam(testapi.Default.Version()) + "=name%3Dfoo&timeout=1s"
	fakeHandler.ValidateRequest(t, requestURL, "POST", &tmpStr)
}

func TestDoRequestNewWayFile(t *testing.T) {
	reqObj := &api.Pod{ObjectMeta: api.ObjectMeta{Name: "foo"}}
	reqBodyExpected, err := testapi.Default.Codec().Encode(reqObj)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	file, err := ioutil.TempFile("", "foo")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	_, err = file.Write(reqBodyExpected)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	expectedObj := &api.Service{Spec: api.ServiceSpec{Ports: []api.ServicePort{{
		Protocol:   "TCP",
		Port:       12345,
		TargetPort: intstr.FromInt(12345),
	}}}}
	expectedBody, _ := testapi.Default.Codec().Encode(expectedObj)
	fakeHandler := util.FakeHandler{
		StatusCode:   200,
		ResponseBody: string(expectedBody),
		T:            t,
	}
	testServer := httptest.NewServer(&fakeHandler)
	defer testServer.Close()
	c := testRESTClient(t, testServer)
	wasCreated := true
	obj, err := c.Verb("POST").
		Prefix("foo/bar", "baz").
		Timeout(time.Second).
		Body(file.Name()).
		Do().WasCreated(&wasCreated).Get()
	if err != nil {
		t.Errorf("Unexpected error: %v %#v", err, err)
		return
	}
	if obj == nil {
		t.Error("nil obj")
	} else if !api.Semantic.DeepDerivative(expectedObj, obj) {
		t.Errorf("Expected: %#v, got %#v", expectedObj, obj)
	}
	if wasCreated {
		t.Errorf("expected object was not created")
	}
	tmpStr := string(reqBodyExpected)
	requestURL := testapi.Default.ResourcePathWithPrefix("foo/bar/baz", "", "", "")
	requestURL += "?timeout=1s"
	fakeHandler.ValidateRequest(t, requestURL, "POST", &tmpStr)
}

func TestWasCreated(t *testing.T) {
	reqObj := &api.Pod{ObjectMeta: api.ObjectMeta{Name: "foo"}}
	reqBodyExpected, err := testapi.Default.Codec().Encode(reqObj)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	expectedObj := &api.Service{Spec: api.ServiceSpec{Ports: []api.ServicePort{{
		Protocol:   "TCP",
		Port:       12345,
		TargetPort: intstr.FromInt(12345),
	}}}}
	expectedBody, _ := testapi.Default.Codec().Encode(expectedObj)
	fakeHandler := util.FakeHandler{
		StatusCode:   201,
		ResponseBody: string(expectedBody),
		T:            t,
	}
	testServer := httptest.NewServer(&fakeHandler)
	defer testServer.Close()
	c := testRESTClient(t, testServer)
	wasCreated := false
	obj, err := c.Verb("PUT").
		Prefix("foo/bar", "baz").
		Timeout(time.Second).
		Body(reqBodyExpected).
		Do().WasCreated(&wasCreated).Get()
	if err != nil {
		t.Errorf("Unexpected error: %v %#v", err, err)
		return
	}
	if obj == nil {
		t.Error("nil obj")
	} else if !api.Semantic.DeepDerivative(expectedObj, obj) {
		t.Errorf("Expected: %#v, got %#v", expectedObj, obj)
	}
	if !wasCreated {
		t.Errorf("Expected object was created")
	}

	tmpStr := string(reqBodyExpected)
	requestURL := testapi.Default.ResourcePathWithPrefix("foo/bar/baz", "", "", "")
	requestURL += "?timeout=1s"
	fakeHandler.ValidateRequest(t, requestURL, "PUT", &tmpStr)
}

func TestVerbs(t *testing.T) {
	c := testRESTClient(t, nil)
	if r := c.Post(); r.verb != "POST" {
		t.Errorf("Post verb is wrong")
	}
	if r := c.Put(); r.verb != "PUT" {
		t.Errorf("Put verb is wrong")
	}
	if r := c.Get(); r.verb != "GET" {
		t.Errorf("Get verb is wrong")
	}
	if r := c.Delete(); r.verb != "DELETE" {
		t.Errorf("Delete verb is wrong")
	}
}

func TestAbsPath(t *testing.T) {
	expectedPath := "/bar/foo"
	c := testRESTClient(t, nil)
	r := c.Post().Prefix("/foo").AbsPath(expectedPath)
	if r.path != expectedPath {
		t.Errorf("unexpected path: %s, expected %s", r.path, expectedPath)
	}
}

func TestUintParam(t *testing.T) {
	table := []struct {
		name      string
		testVal   uint64
		expectStr string
	}{
		{"foo", 31415, "http://localhost?foo=31415"},
		{"bar", 42, "http://localhost?bar=42"},
		{"baz", 0, "http://localhost?baz=0"},
	}

	for _, item := range table {
		u, _ := url.Parse("http://localhost")
		r := NewRequest(nil, "GET", u, "test", nil).AbsPath("").UintParam(item.name, item.testVal)
		if e, a := item.expectStr, r.URL().String(); e != a {
			t.Errorf("expected %v, got %v", e, a)
		}
	}
}

func TestUnacceptableParamNames(t *testing.T) {
	table := []struct {
		name          string
		testVal       string
		expectSuccess bool
	}{
		{"timeout", "42", false},
	}

	for _, item := range table {
		c := testRESTClient(t, nil)
		r := c.Get().setParam(item.name, item.testVal)
		if e, a := item.expectSuccess, r.err == nil; e != a {
			t.Errorf("expected %v, got %v (%v)", e, a, r.err)
		}
	}
}

func TestBody(t *testing.T) {
	const data = "test payload"

	obj := &api.Pod{ObjectMeta: api.ObjectMeta{Name: "foo"}}
	bodyExpected, _ := testapi.Default.Codec().Encode(obj)

	f, err := ioutil.TempFile("", "test_body")
	if err != nil {
		t.Fatalf("TempFile error: %v", err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("TempFile.WriteString error: %v", err)
	}
	f.Close()

	c := testRESTClient(t, nil)
	tests := []struct {
		input    interface{}
		expected string
		headers  map[string]string
	}{
		{[]byte(data), data, nil},
		{f.Name(), data, nil},
		{strings.NewReader(data), data, nil},
		{obj, string(bodyExpected), map[string]string{"Content-Type": "application/json"}},
	}
	for i, tt := range tests {
		r := c.Post().Body(tt.input)
		if r.err != nil {
			t.Errorf("%d: r.Body(%#v) error: %v", i, tt, r.err)
			continue
		}
		buf := make([]byte, len(tt.expected))
		if _, err := r.body.Read(buf); err != nil {
			t.Errorf("%d: r.body.Read error: %v", i, err)
			continue
		}
		body := string(buf)
		if body != tt.expected {
			t.Errorf("%d: r.body = %q; want %q", i, body, tt.expected)
		}
		if tt.headers != nil {
			for k, v := range tt.headers {
				if r.headers.Get(k) != v {
					t.Errorf("%d: r.headers[%q] = %q; want %q", i, k, v, v)
				}
			}
		}
	}
}

func TestWatch(t *testing.T) {
	var table = []struct {
		t   watch.EventType
		obj runtime.Object
	}{
		{watch.Added, &api.Pod{ObjectMeta: api.ObjectMeta{Name: "first"}}},
		{watch.Modified, &api.Pod{ObjectMeta: api.ObjectMeta{Name: "second"}}},
		{watch.Deleted, &api.Pod{ObjectMeta: api.ObjectMeta{Name: "last"}}},
	}

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			panic("need flusher!")
		}

		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		encoder := watchjson.NewEncoder(w, testapi.Default.Codec())
		for _, item := range table {
			if err := encoder.Encode(&watch.Event{Type: item.t, Object: item.obj}); err != nil {
				panic(err)
			}
			flusher.Flush()
		}
	}))
	defer testServer.Close()

	s := testRESTClient(t, testServer)
	watching, err := s.Get().Prefix("path/to/watch/thing").Watch()
	if err != nil {
		t.Fatalf("Unexpected error")
	}

	for _, item := range table {
		got, ok := <-watching.ResultChan()
		if !ok {
			t.Fatalf("Unexpected early close")
		}
		if e, a := item.t, got.Type; e != a {
			t.Errorf("Expected %v, got %v", e, a)
		}
		if e, a := item.obj, got.Object; !api.Semantic.DeepDerivative(e, a) {
			t.Errorf("Expected %v, got %v", e, a)
		}
	}

	_, ok := <-watching.ResultChan()
	if ok {
		t.Fatal("Unexpected non-close")
	}
}

func TestStream(t *testing.T) {
	expectedBody := "expected body"

	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			panic("need flusher!")
		}
		w.Header().Set("Transfer-Encoding", "chunked")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(expectedBody))
		flusher.Flush()
	}))
	defer testServer.Close()

	s := testRESTClient(t, testServer)
	readCloser, err := s.Get().Prefix("path/to/stream/thing").Stream()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer readCloser.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(readCloser)
	resultBody := buf.String()

	if expectedBody != resultBody {
		t.Errorf("Expected %s, got %s", expectedBody, resultBody)
	}
}

func testRESTClient(t testing.TB, srv *httptest.Server) *RESTClient {
	baseURL, _ := url.Parse("http://localhost")
	if srv != nil {
		var err error
		baseURL, err = url.Parse(srv.URL)
		if err != nil {
			t.Fatalf("failed to parse test URL: %v", err)
		}
	}
	baseURL.Path = testapi.Default.ResourcePath("", "", "")
	return NewRESTClient(baseURL, testapi.Default.GroupVersion().String(), testapi.Default.Codec(), 0, 0)
}
