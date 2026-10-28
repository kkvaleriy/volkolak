package workflows

import (
	"context"
	"sync"
	"testing"

	"github.com/kkvaleriy/volkolak/internal/activities/analyze"
	"github.com/kkvaleriy/volkolak/internal/activities/media"
	"github.com/kkvaleriy/volkolak/internal/activities/store"
	"github.com/kkvaleriy/volkolak/internal/activities/transcribe"
	"github.com/kkvaleriy/volkolak/internal/activities/unpack"
	"github.com/kkvaleriy/volkolak/internal/pipeline"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestArchiveWorkflowOrder(t *testing.T) {
	result := runArchive(t, ArchiveInput{
		Key: "archives/1/film.zip", Filename: "film.zip",
		ActivityTaskQueue: "volkolak-activity", SceneTaskQueue: "volkolak-scene", TranscribeTaskQueue: "volkolak-transcribe",
	})
	require.Equal(t, "zip", result.ArchiveType)
	require.Equal(t, 1, result.RemovedFiles)
	require.Len(t, result.Videos, 1)
	require.Equal(t, "clip.mp4", result.Videos[0].Filename)
	require.Equal(t, "11111111-1111-1111-1111-111111111111", result.Videos[0].VideoID)
	require.Equal(t, "jobs/test/extracted/clip.mp4", result.Videos[0].ObjectKey)
	require.Equal(t, "ru", result.Videos[0].Language)
	require.Equal(t, "привет", result.Videos[0].Transcript)
	require.Equal(t, 1, result.Videos[0].FrameCount)
	require.True(t, result.Videos[0].HasAudio)
}

func TestArchiveWorkflowWithoutVideos(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ArchiveWorkflow)
	env.RegisterWorkflow(VideoWorkflow)
	env.OnActivity(new(unpack.Activities).DetectArchiveType, mock.Anything, mock.Anything).Return("zip", nil).Once()
	env.OnActivity(new(unpack.Activities).ExtractVideos, mock.Anything, mock.Anything).Return(unpack.ExtractResult{
		Videos:  []pipeline.VideoFile{},
		Removed: 4,
	}, nil).Once()

	env.ExecuteWorkflow(ArchiveWorkflow, ArchiveInput{
		Key: "archives/empty.zip", Filename: "empty.zip",
		ActivityTaskQueue: "volkolak-activity", SceneTaskQueue: "volkolak-scene", TranscribeTaskQueue: "volkolak-transcribe",
	})
	require.NoError(t, env.GetWorkflowError())
	var result ArchiveResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Empty(t, result.Videos)
	require.Equal(t, 4, result.RemovedFiles)
	env.AssertExpectations(t)
}

func TestSafeSegment(t *testing.T) {
	require.Equal(t, "archive-abc", safeSegment("archive-abc"))
	require.Equal(t, "a_b", safeSegment("a/b"))
	require.Equal(t, "wf", safeSegment(""))
	require.Equal(t, "__", safeSegment(".."))
}

func runArchive(t *testing.T, input ArchiveInput) ArchiveResult {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ArchiveWorkflow)
	env.RegisterWorkflow(VideoWorkflow)

	var mu sync.Mutex
	var order []string
	note := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	env.OnActivity(new(unpack.Activities).DetectArchiveType, mock.Anything, input.Key).
		Return(func(context.Context, string) (string, error) {
			note("detect")
			return "zip", nil
		}).Once()
	env.OnActivity(new(unpack.Activities).ExtractVideos, mock.Anything, mock.Anything).
		Return(func(context.Context, unpack.ExtractInput) (unpack.ExtractResult, error) {
			note("extract")
			return unpack.ExtractResult{
				Videos: []pipeline.VideoFile{{
					Key:          "jobs/test/extracted/clip.mp4",
					RelativePath: "clip.mp4",
					Filename:     "clip.mp4",
				}},
				Removed: 1,
			}, nil
		}).Once()

	env.OnActivity(new(store.Activities).SaveVideo, mock.Anything, mock.Anything).
		Return(func(_ context.Context, in store.SaveVideoInput) (store.SaveVideoResult, error) {
			note("save-video")
			if in.OriginalFilename != "clip.mp4" || in.ObjectKey == "" {
				return store.SaveVideoResult{}, errString("video " + in.OriginalFilename)
			}
			return store.SaveVideoResult{VideoID: "11111111-1111-1111-1111-111111111111"}, nil
		}).Once()
	env.OnActivity(new(media.Activities).ExtractAudio, mock.Anything, mock.Anything).
		Return(func(context.Context, media.ExtractAudioInput) (media.ExtractAudioResult, error) {
			note("audio")
			return media.ExtractAudioResult{AudioKey: "jobs/x/audio.wav", HasAudio: true}, nil
		}).Once()
	env.OnActivity(new(media.Activities).ExtractFrames, mock.Anything, mock.Anything).
		Return(func(context.Context, media.ExtractFramesInput) (media.ExtractFramesResult, error) {
			note("frames")
			return media.ExtractFramesResult{Frames: []pipeline.Frame{{
				Index: 0, TimestampSec: 0, Key: "jobs/x/frames/frame_0001.jpg",
			}}}, nil
		}).Once()
	env.OnActivity(new(analyze.Activities).RecognizeText, mock.Anything, mock.Anything).
		Return(func(context.Context, analyze.AnalyzeInput) (analyze.AnalyzeResult, error) {
			note("ocr")
			return analyze.AnalyzeResult{Frames: []pipeline.FrameAnalysis{{
				Index: 0, Key: "jobs/x/frames/frame_0001.jpg", OCRText: "титр",
			}}}, nil
		}).Once()
	env.OnActivity(new(analyze.Activities).DescribeScenes, mock.Anything, mock.Anything).
		Return(func(context.Context, analyze.AnalyzeInput) (analyze.AnalyzeResult, error) {
			note("scenes")
			return analyze.AnalyzeResult{Frames: []pipeline.FrameAnalysis{{
				Index: 0, Key: "jobs/x/frames/frame_0001.jpg", Description: "комната",
			}}}, nil
		}).Once()
	env.OnActivity(new(store.Activities).SaveFrameResults, mock.Anything, mock.Anything).
		Return(func(context.Context, store.SaveFramesInput) error {
			note("save-frames")
			return nil
		}).Once()
	env.OnActivity(new(transcribe.Activities).Transcribe, mock.Anything, mock.Anything).
		Return(func(context.Context, transcribe.TranscribeInput) (pipeline.Transcript, error) {
			note("transcribe")
			return pipeline.Transcript{Language: "ru", Text: "привет", HasAudio: true, Segments: []pipeline.Segment{{Text: "привет"}}}, nil
		}).Once()
	env.OnActivity(new(store.Activities).SaveTranscript, mock.Anything, mock.Anything).
		Return(func(context.Context, store.SaveTranscriptInput) error {
			note("save-transcript")
			return nil
		}).Once()

	env.ExecuteWorkflow(ArchiveWorkflow, input)
	require.NoError(t, env.GetWorkflowError())
	var result ArchiveResult
	require.NoError(t, env.GetWorkflowResult(&result))
	require.Equal(t, []string{
		"detect", "extract", "save-video", "audio", "frames", "ocr", "scenes", "save-frames", "transcribe", "save-transcript",
	}, order)
	env.AssertExpectations(t)
	return result
}

type errString string

func (e errString) Error() string { return string(e) }
