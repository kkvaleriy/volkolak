package media

import "math"

const scaleFilter = "scale=w='min(1280,iw)':h=-2"

// frameTimestamps выбирает моменты так, чтобы кадры покрывали всё видео.
// Базовый шаг — intervalSec. Если кадров получается больше maxFrames,
// шаг увеличивается и по ролику раскладывается ровно maxFrames точек.
// duration <= 0 означает, что длительность неизвестна: вызывающий код
// переключается на выборку с начала файла.
func frameTimestamps(duration float64, intervalSec, maxFrames int) []float64 {
	if duration <= 0 {
		return nil
	}
	if intervalSec < 1 {
		intervalSec = 1
	}
	if maxFrames < 1 {
		maxFrames = 1
	}

	step := float64(intervalSec)
	n := int(math.Floor((duration-0.001)/step)) + 1
	if n < 1 {
		n = 1
	}
	if n > maxFrames {
		n = maxFrames
		if n == 1 {
			return []float64{0}
		}
		step = duration / float64(n)
	}

	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		ts := float64(i) * step
		if ts >= duration {
			break
		}
		out = append(out, ts)
	}
	if len(out) == 0 {
		return []float64{0}
	}
	return out
}
