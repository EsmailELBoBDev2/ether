package transcode

import (
	"net/url"
	"slices"
	"testing"
)

func TestLiveDecoderInputArgsSetsHLSFormatBeforeLiveStartIndex(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:8080/relay/stream")
	if err != nil {
		t.Fatal(err)
	}
	args := liveDecoderInputArgs(DecoderRequest{InputFormat: "hls"}, u)
	idx := slices.Index(args, "-live_start_index")
	if idx < 2 || args[idx-2] != "-f" || args[idx-1] != "hls" {
		t.Fatalf("expected -f hls immediately before -live_start_index, got %v", args)
	}
}
