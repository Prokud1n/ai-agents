#!/bin/sh
# Checks of the roughcut binary inside the media image (CI job media-image), with the image's ffmpeg.
# DATA is a directory shared between the runs (the load clip is made once, without a memory limit):
#
#   sh roughcut-image-check.sh basic DATA   # rotation tag and a video without sound
#   sh roughcut-image-check.sh prepare DATA # the load clip: 1080x1920, 120 s, 60 words
#   sh roughcut-image-check.sh load DATA    # rough cut of the load clip, peak memory under MAX_PEAK_KB
#
# basic: a phone video stored as 1920x1080 with a 90° rotation tag comes out 1080x1920; a video without
# sound is refused with a clear error before anything is written.
# load: the peak memory of the container (cgroup memory.peak: roughcut, ffmpeg and their page cache) must
# stay under MAX_PEAK_KB (800 MiB); where memory.peak is missing, the sampled resident memory of roughcut and
# ffmpeg is the measure.
# POSIX sh: the image has busybox only.
set -eu

MODE=$1
DATA=$2
MAX_PEAK_KB=${MAX_PEAK_KB:-819200}
CGROUP_PEAK=/sys/fs/cgroup/memory.peak
cd "$DATA"

ff() { ffmpeg -hide_banner -nostdin -loglevel error -y "$@"; }

# words N STEP LEN: a transcript of N words, word i at 0.5+i*STEP for LEN seconds; also prints the tone
# expression for aevalsrc to tones.txt.
words() {
	awk -v n="$1" -v step="$2" -v len="$3" 'BEGIN {
		printf "{\"language\":\"ru\",\"duration\":0,\"text\":\"\",\"segments\":[],\"words\":["
		expr = "0"
		for (i = 0; i < n; i++) {
			s = 0.5 + i * step; e = s + len
			printf "%s{\"word\":\"слово%d\",\"start\":%.3f,\"end\":%.3f}", (i ? "," : ""), i, s, e
			expr = expr sprintf("+between(t\\,%.3f\\,%.3f)", s, e)
		}
		printf "]}"
		print expr > "tones.txt"
	}' > transcript.json
}

tone() { printf 'aevalsrc=if(%s\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=%s' "$(cat tones.txt)" "$1"; }

size_of() { ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$1"; }

# rss_kb: the resident memory of all roughcut and ffmpeg processes, in KiB.
rss_kb() {
	total=0
	for status in /proc/[0-9]*/status; do
		name=$(awk '/^Name:/ { print $2 }' "$status" 2>/dev/null || true)
		case "$name" in
		roughcut | ffmpeg)
			kb=$(awk '/^VmRSS:/ { print $2 }' "$status" 2>/dev/null || true)
			total=$((total + ${kb:-0}))
			;;
		esac
	done
	echo "$total"
}

basic() {
	echo "== rotation"
	words 4 1.5 0.6
	ff -f lavfi -i "$(tone 7)" -f lavfi -i "testsrc=duration=7:size=1920x1080:rate=30" \
		-c:v libx264 -preset ultrafast -c:a aac -shortest plain.mp4
	ff -display_rotation 90 -i plain.mp4 -c copy rotated.mp4
	/app/roughcut -in rotated.mp4 -transcript transcript.json -out rotated
	size=$(size_of rotated/roughcut.mp4)
	echo "rotated source -> $size"
	[ "$size" = "1080,1920" ] || { echo "expected 1080,1920"; exit 1; }

	echo "== no audio"
	ff -f lavfi -i "testsrc=duration=3:size=360x640:rate=30" -c:v libx264 -preset ultrafast silent.mp4
	if /app/roughcut -in silent.mp4 -transcript transcript.json -out silent > silent.log 2>&1; then
		echo "a video without sound must be refused"; exit 1
	fi
	cat silent.log
	grep -q "source has no audio stream" silent.log
	[ ! -e silent/roughcut.mp4 ] && [ ! -e silent/plan.json ]

	echo "basic checks OK"
}

prepare() {
	echo "== load clip: 1080x1920, 120 s, 60 words"
	words 60 2 0.6
	ff -f lavfi -i "$(tone 121)" -f lavfi -i "testsrc=duration=121:size=1080x1920:rate=30" \
		-c:v libx264 -preset ultrafast -c:a aac -shortest load.mp4
	ls -l load.mp4
}

load() {
	echo "== load: rough cut in 60 fragments"
	/app/roughcut -in load.mp4 -transcript transcript.json -out load > load.log 2>&1 &
	pid=$!
	peak=0
	while kill -0 "$pid" 2>/dev/null; do
		now=$(rss_kb)
		[ "$now" -gt "$peak" ] && peak=$now
		sleep 0.2
	done
	status=0
	wait "$pid" || status=$?
	cat load.log
	echo "sampled resident memory of roughcut + ffmpeg: ${peak} KiB, exit ${status}"
	[ "$status" -eq 0 ] || { echo "roughcut failed"; exit 1; }
	if [ -r "$CGROUP_PEAK" ]; then
		peak=$(($(cat "$CGROUP_PEAK") / 1024))
		echo "cgroup memory.peak: ${peak} KiB"
	fi
	echo "peak memory: ${peak} KiB (limit ${MAX_PEAK_KB} KiB)"
	[ "$peak" -le "$MAX_PEAK_KB" ] || { echo "peak memory above the limit"; exit 1; }
	grep -q '"fragments":60' load.log || { echo "expected 60 fragments"; exit 1; }
	d=$(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 load/roughcut.mp4)
	echo "load result: $(size_of load/roughcut.mp4), ${d} s"
	awk -v d="$d" 'BEGIN { exit !(d > 40 && d < 50) }' || { echo "unexpected duration"; exit 1; }
	echo "load check OK"
}

case "$MODE" in
basic) basic ;;
prepare) prepare ;;
load) load ;;
*) echo "usage: roughcut-image-check.sh basic|prepare|load DATA"; exit 2 ;;
esac
