package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/Shopify/sarama"
)

var (
	invalidClientIDCharactersRegExp = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

func listenForInterrupt(q chan struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Kill, os.Interrupt)
	sig := <-signals
	fmt.Fprintf(os.Stderr, "received signal %s\n", sig)
	close(q)
}

func kafkaVersion(s string) sarama.KafkaVersion {
	dflt := sarama.V0_10_0_0
	switch s {
	case "v0.8.2.0":
		return sarama.V0_8_2_0
	case "v0.8.2.1":
		return sarama.V0_8_2_1
	case "v0.8.2.2":
		return sarama.V0_8_2_2
	case "v0.9.0.0":
		return sarama.V0_9_0_0
	case "v0.9.0.1":
		return sarama.V0_9_0_1
	case "v0.10.0.0":
		return sarama.V0_10_0_0
	case "v0.10.0.1":
		return sarama.V0_10_0_1
	case "v0.10.1.0":
		return sarama.V0_10_1_0
	case "v0.10.2.0":
		return sarama.V0_10_2_0
	case "":
		return dflt
	}

	failf("unsupported kafka version %#v - supported: v0.8.2.0, v0.8.2.1, v0.8.2.2, v0.9.0.0, v0.9.0.1, v0.10.0.0, v0.10.0.1, v0.10.1.0, v0.10.2.0", s)
	return dflt
}

func logClose(name string, c io.Closer) {
	if err := c.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to close %#v err=%v", name, err)
	}
}

type printContext struct {
	output interface{}
	done   chan struct{}
}

func print(in <-chan printContext, pretty bool) {
	var (
		buf     []byte
		err     error
		marshal = json.Marshal
	)

	if pretty && terminal.IsTerminal(int(syscall.Stdout)) {
		marshal = func(i interface{}) ([]byte, error) { return json.MarshalIndent(i, "", "  ") }
	}

	for {
		ctx := <-in
		if buf, err = marshal(ctx.output); err != nil {
			failf("failed to marshal output %#v, err=%v", ctx.output, err)
		}

		fmt.Println(string(buf))
		close(ctx.done)
	}
}

func failf(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, msg+"\n", args...)
	os.Exit(1)
}

func readStdinLines(max int, out chan string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, max), max)

	for scanner.Scan() {
		out <- scanner.Text()
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "scanning input failed err=%v\n", err)
	}
	close(out)
}

// hashCode imitates the behavior of the JDK's String#hashCode method.
// https://docs.oracle.com/javase/7/docs/api/java/lang/String.html#hashCode()
//
// As strings are encoded in utf16 on the JVM, this implementation checks wether
// s contains non-bmp runes and uses utf16 surrogate pairs for those.
func hashCode(s string) (hc int32) {
	for _, r := range s {
		r1, r2 := utf16.EncodeRune(r)
		if r1 == 0xfffd && r1 == r2 {
			hc = hc*31 + r
		} else {
			hc = (hc*31+r1)*31 + r2
		}
	}
	return
}

func kafkaAbs(i int32) int32 {
	switch {
	case i == -2147483648: // Integer.MIN_VALUE
		return 0
	case i < 0:
		return i * -1
	default:
		return i
	}
}

func hashCodePartition(key string, partitions int32) int32 {
	if partitions <= 0 {
		return -1
	}

	return kafkaAbs(hashCode(key)) % partitions
}

func sanitizeUsername(u string) string {
	// Windows user may have format "DOMAIN|MACHINE\username", remove domain/machine if present
	s := strings.Split(u, "\\")
	u = s[len(s)-1]
	// Windows account can contain spaces or other special characters not supported
	// in client ID. Keep the bare minimum and ditch the rest.
	return invalidClientIDCharactersRegExp.ReplaceAllString(u, "")
}

func randomString(length int) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	buf := make([]byte, length)
	r.Read(buf)
	return fmt.Sprintf("%x", buf)[:length]
}

func getTransformValue(name, envvar, argvalue string) (string, error) {
	value := argvalue
	if value == "" {
		value = os.Getenv(envvar)
	}
	switch value {
	case "string", "hex", "base64":
		return value, nil
	case "":
		return "string", nil
	default:
		return "", fmt.Errorf(`unsupported %s argument %#v, only string, hex and base64 are supported`, name, value)
	}
}
