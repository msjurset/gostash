package gemini

import "fmt"

// DefaultIdentifyPrompt is the same prompt the Mac and Android
// clients use. Kept in sync because all three clients share the
// TITLE/NOTES/TRANSCRIPT parser — any change to the marker shape
// here MUST land in stash-mac (AIProvider.swift) and droid_stash
// (GeminiClient.kt) at the same time, or one platform's output
// stops parsing.
const DefaultIdentifyPrompt = `Identify the main subject in this photo.

Respond with exactly these three lines, no preamble, no markdown:

TITLE: <common name; include scientific name in parentheses when applicable>
NOTES: <natural prose, three to six sentences. Open by naming the subject in plain language — e.g. "This is the YYYY mushroom (Scientificus nameus), also known as XXXX..." or "This is the eastern bluebird (Sialia sialis), a small thrush native to..." Then cover, where relevant: notable visual characteristics; habitat, range, or season; edibility / toxicity / safety; species commonly confused with it; what specific features visible in this photo helped identify it; and any other interesting facts a curious naturalist would want to know. Be generous with detail — the user will trim what they don't want.>
TRANSCRIPT: <if the photo contains readable text — printed, typed, OR handwritten (including cursive) — transcribe it verbatim here, preserving line breaks where they're meaningful. Cover the entire visible text, not just a sample. If the image contains no meaningful text (e.g. it's a flower, animal, landscape with no signs / labels / writing), write exactly NONE.>

If you can't identify confidently, write TITLE: Unknown and explain your best guess and the reasoning in NOTES.`

const DefaultTranscribePrompt = `Transcribe this audio recording exactly.

Respond with exactly these three lines, no preamble, no markdown:

TITLE: <a descriptive title for the recording based on its content, maximum 60 characters>
NOTES: <one or two sentences describing the tone, context, or key takeaway of the audio>
TRANSCRIPT: <the verbatim transcript of every word spoken, preserving natural speech flow and line breaks where they're meaningful.

CRITICAL SPEAKER RULES:
1. Carefully analyze if the recording contains only one person speaking, or multiple distinct voices engaged in conversation.
2. If there is ONLY ONE speaker throughout the entire recording (or if you are not absolutely certain there are multiple distinct people talking), you MUST NOT use any speaker labels, names, or prefixes (such as "SPEAKER 1:", "Speaker 1:", "Speaker:", etc.) anywhere in the transcript. Simply transcribe the speech as continuous plain paragraphs.
3. A single speaker pausing, changing their tone, or speaking after a silence must NOT be given a label or split into multiple speakers. Keep them as one continuous stream of text.
4. Only use speaker labels (e.g., "SPEAKER 1:", "SPEAKER 2:") if there is an actual conversation, interview, or Q&A dialogue between two or more different people.>

If the audio is not spoken words (e.g. ambient noise, music, or silence), write TITLE: Audio Capture and describe what you hear in NOTES.`

const DefaultVideoTranscribePrompt = `Identify the subject and transcribe any speech in this video.

Respond with exactly these three lines, no preamble, no markdown:

TITLE: <a descriptive title for the video based on its content, maximum 60 characters>
NOTES: <natural prose, three to six sentences describing the visual subject and context of the video>
TRANSCRIPT: <the verbatim transcript of every word spoken in the video, preserving natural speech flow and line breaks where they're meaningful. If no words are spoken, write NONE.

CRITICAL SPEAKER RULES:
1. Carefully analyze if the recording contains only one person speaking, or multiple distinct voices engaged in conversation.
2. If there is ONLY ONE speaker throughout the entire recording (or if you are not absolutely certain there are multiple distinct people talking), you MUST NOT use any speaker labels, names, or prefixes (such as "SPEAKER 1:", "Speaker 1:", "Speaker:", etc.) anywhere in the transcript. Simply transcribe the speech as continuous plain paragraphs.
3. A single speaker pausing, changing their tone, or speaking after a silence must NOT be given a label or split into multiple speakers. Keep them as one continuous stream of text.
4. Only use speaker labels (e.g., "SPEAKER 1:", "SPEAKER 2:") if there is an actual conversation, interview, or Q&A dialogue between two or more different people.>

If the video is silent and has no clear subject, write TITLE: Video Capture and describe what you see in NOTES.`

// MultiImageHint is prepended to the prompt when an identify call
// carries more than one image — tells Gemini they're the same
// subject from different angles/states, not N unrelated subjects.
// Without this, a multi-angle mushroom shot comes back as "I see
// three different fungi" instead of one identification.
func MultiImageHint(count int) string {
	return fmt.Sprintf(
		"The following %d photos are of the same subject from different angles or states. Identify the subject using all photos together.\n\n",
		count,
	)
}
