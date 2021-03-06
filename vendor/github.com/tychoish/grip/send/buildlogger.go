package send

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tychoish/grip/level"
	"github.com/tychoish/grip/message"
)

type buildlogger struct {
	conf   *BuildloggerConfig
	name   string
	testID string
	client *http.Client
	*base
}

// BuildloggerConfig describes the configuration needed for a Sender
// instance that posts log messages to a buildlogger service
// (e.g. logkeeper.)
type BuildloggerConfig struct {
	// CreateTest controls
	CreateTest bool
	URL        string

	// The following values are used by the buildlogger service to
	// attach metadata to the logs. The GetBuildloggerConfig
	// method populates Number, Phase, Builder, and Test from
	// environment variables, though you can set them directly in
	// your application. You must set the Command value directly.
	Number  int
	Phase   string
	Builder string
	Test    string
	Command string

	// Configure a local sender for "fallback" operations and to
	// collect the location (URLS) of the buildlogger output
	Local Sender

	buildID  string
	username string
	password string
}

// ReadCredentialsFromFile parses a JSON file for buildlogger
// credentials and updates the config.
func (c *BuildloggerConfig) ReadCredentialsFromFile(fn string) error {
	if _, err := os.Stat(fn); os.IsNotExist(err) {
		return errors.New("credentials file does not exist")
	}

	contents, err := ioutil.ReadFile(fn)
	if err != nil {
		return err
	}

	out := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{}
	if err := json.Unmarshal(contents, &out); err != nil {
		return err
	}

	c.username = out.Username
	c.password = out.Password

	return nil
}

// SetCredentials configures the username and password of the
// BuildLoggerConfig object. Use to programatically update the
// credentials in a configuration object instead of reading from the
// credentials file with ReadCredentialsFromFile(),
func (c *BuildloggerConfig) SetCredentials(username, password string) {
	c.username = username
	c.password = password
}

// GetBuildloggerConfig produces a BuildloggerConfig object, reading
// default values from environment variables, although you can set
// these options yourself.
//
// You must also populate the credentials seperatly using either the
// ReadCredentialsFromFile or SetCredentials methods. If the
// BUILDLOGGER_CREDENTIALS environment variable is set,
// GetBuildloggerConfig will read credentials from this file.
//
// Buildlogger has a concept of a build, with a global log, as well as
// subsidiary "test" logs. To exercise this functionality, you will
// use a single Buildlogger config instance to create individual
// Sender instances that target each of these output formats.
//
// Create a BuildloggerConfig instance, set up the crednetials if
// needed, and create a sender. This will be the "global" log, in
// buildlogger terminology. Then, set set the CreateTest attribute,
// and generate additional per-test senders. For example:
//
//    conf := GetBuildloggerConfig()
//    global := MakeBuildlogger("<name>-global", conf)
//    // ... use global
//    conf.CreateTest = true
//    testOne := MakeBuildlogger("<name>-testOne", conf)
func GetBuildloggerConfig() (*BuildloggerConfig, error) {
	conf := &BuildloggerConfig{
		URL:     os.Getenv("BULDLOGGER_URL"),
		Phase:   os.Getenv("MONGO_PHASE"),
		Builder: os.Getenv("MONGO_BUILDER_NAME"),
		Test:    os.Getenv("MONGO_TEST_FILENAME"),
	}

	if creds := os.Getenv("BUILDLOGGER_CREDENTIALS"); creds != "" {
		if err := conf.ReadCredentialsFromFile(creds); err != nil {
			return nil, err
		}
	}

	buildNum, err := strconv.Atoi(os.Getenv("MONGO_BUILD_NUMBER"))
	if err != nil {
		return nil, err
	}
	conf.Number = buildNum

	if conf.Test == "" {
		conf.Test = "unknown"
	}

	if conf.Phase == "" {
		conf.Phase = "unknown"
	}

	return conf, nil
}

// NewBuildlogger constructs a Buildlogger-targeted Sender, with level
// information set. See MakeBuildlogger and GetBuildloggerConfig for
// more information.
func NewBuildlogger(name string, conf *BuildloggerConfig, l LevelInfo) (Sender, error) {
	s, err := MakeBuildlogger(name, conf)
	if err != nil {
		return nil, err
	}

	if err := s.SetLevel(l); err != nil {
		return nil, err
	}

	return s, nil
}

// MakeBuildlogger constructs a buildlogger targeting sender using the
// BuildloggerConfig object for configuration. Generally you will
// create a "global" instance, and then several subsidiary Senders
// that target specific tests. See the documentation of
// GetBuildloggerConfig for more information.
//
// Upon creating a logger, this method will write, to standard out,
// the URL that you can use to view the logs produced by this Sender.
func MakeBuildlogger(name string, conf *BuildloggerConfig) (Sender, error) {
	b := &buildlogger{
		name:   name,
		conf:   conf,
		client: &http.Client{Timeout: 10 * time.Second},
		base:   newBase(name),
	}

	if b.conf.Local == nil {
		b.conf.Local = MakeNative()
	}

	if b.conf.buildID == "" {
		data := struct {
			Builder string `json:"builder"`
			Number  int    `json:"buildnum"`
		}{
			Builder: name,
			Number:  conf.Number,
		}

		out, err := b.doPost(data)
		if err != nil {
			b.conf.Local.Send(message.NewErrorMessage(level.Error, err))
			return nil, err
		}

		b.conf.buildID = out.ID

		b.conf.Local.Send(message.NewFormattedMessage(level.Notice,
			"Writing logs to buildlogger global log at %s/build/%s",
			b.conf.URL, b.conf.buildID))
	}

	if b.conf.CreateTest {
		data := struct {
			Filename string `json:"test_filename"`
			Command  string `json:"command"`
			Phase    string `json:"phase"`
		}{
			Filename: conf.Test,
			Command:  conf.Command,
			Phase:    conf.Phase,
		}

		out, err := b.doPost(data)
		if err != nil {
			b.conf.Local.Send(message.NewErrorMessage(level.Error, err))
			return nil, err
		}

		b.testID = out.ID

		b.conf.Local.Send(message.NewFormattedMessage(level.Notice,
			"Writing logs to buildlogger test log at %s/build/%s/test/%s",
			conf.URL, b.conf.buildID, b.testID))
	}

	return b, nil
}

func (b *buildlogger) Type() SenderType { return Buildlogger }
func (b *buildlogger) Send(m message.Composer) {
	if b.level.ShouldLog(m) {
		msg := m.Resolve()

		line := [][]interface{}{{float64(time.Now().Unix()), msg}}
		out, err := json.Marshal(line)
		if err != nil {
			b.conf.Local.Send(message.NewErrorMessage(m.Priority(), err))
		}

		if err := b.postLines(bytes.NewBuffer(out)); err != nil {
			b.conf.Local.Send(message.NewErrorMessage(m.Priority(), err))
			b.conf.Local.Send(m)
		}
	}
}

func (b *buildlogger) SetLevel(l LevelInfo) error {
	if err := b.base.SetLevel(l); err != nil {
		return err
	}

	_ = b.conf.Local.SetLevel(l)
	return nil
}

///////////////////////////////////////////////////////////////////////////
//
// internal methods and helpers
//
///////////////////////////////////////////////////////////////////////////

type buildLoggerIDResponse struct {
	ID string `json:"id"`
}

func (b *buildlogger) doPost(data interface{}) (*buildLoggerIDResponse, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", b.getURL(), bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.SetBasicAuth(b.conf.username, b.conf.password)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)

	out := &buildLoggerIDResponse{}
	if err := decoder.Decode(out); err != nil {
		return nil, err
	}

	return out, nil
}

func (b *buildlogger) getURL() string {
	parts := []string{b.conf.URL, "build"}

	if b.conf.buildID != "" {
		parts = append(parts, b.conf.buildID)
	}

	// if we want to create a test id, (e.g. the CreateTest flag
	// is set and we don't have a testID), then the following URL
	// will generate a testID.
	if b.conf.CreateTest && b.testID == "" {
		// this will create the testID.
		parts = append(parts, "test")
	}

	// if a test id is present, then we want to append to the test logs.
	if b.testID != "" {
		parts = append(parts, "test", b.testID)
	}

	return strings.Join(parts, "/")
}

func (b *buildlogger) postLines(body io.Reader) error {
	req, err := http.NewRequest("POST", b.getURL(), body)

	if err != nil {
		return err
	}
	req.SetBasicAuth(b.conf.username, b.conf.password)

	_, err = b.client.Do(req)
	return err
}
