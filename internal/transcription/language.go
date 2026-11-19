package transcription

import (
	"strings"
)

// whisperLanguages maps the language names Whisper reports in verbose_json to ISO codes
// (ISO 639-1, or 639-3 where there is no two-letter code), as in Whisper's tokenizer.
var whisperLanguages = map[string]string{
	"afrikaans": "af", "albanian": "sq", "amharic": "am", "arabic": "ar", "armenian": "hy", "assamese": "as",
	"azerbaijani": "az", "bashkir": "ba", "basque": "eu", "belarusian": "be", "bengali": "bn", "bosnian": "bs",
	"breton": "br", "bulgarian": "bg", "cantonese": "yue", "catalan": "ca", "chinese": "zh", "croatian": "hr",
	"czech": "cs", "danish": "da", "dutch": "nl", "english": "en", "estonian": "et", "faroese": "fo",
	"finnish": "fi", "french": "fr", "galician": "gl", "georgian": "ka", "german": "de", "greek": "el",
	"gujarati": "gu", "haitian creole": "ht", "hausa": "ha", "hawaiian": "haw", "hebrew": "he", "hindi": "hi",
	"hungarian": "hu", "icelandic": "is", "indonesian": "id", "italian": "it", "japanese": "ja", "javanese": "jv",
	"kannada": "kn", "kazakh": "kk", "khmer": "km", "korean": "ko", "lao": "lo", "latin": "la", "latvian": "lv",
	"lingala": "ln", "lithuanian": "lt", "luxembourgish": "lb", "macedonian": "mk", "malagasy": "mg",
	"malay": "ms", "malayalam": "ml", "maltese": "mt", "maori": "mi", "marathi": "mr", "mongolian": "mn",
	"myanmar": "my", "nepali": "ne", "norwegian": "no", "nynorsk": "nn", "occitan": "oc", "pashto": "ps",
	"persian": "fa", "polish": "pl", "portuguese": "pt", "punjabi": "pa", "romanian": "ro", "russian": "ru",
	"sanskrit": "sa", "serbian": "sr", "shona": "sn", "sindhi": "sd", "sinhala": "si", "slovak": "sk",
	"slovenian": "sl", "somali": "so", "spanish": "es", "sundanese": "su", "swahili": "sw", "swedish": "sv",
	"tagalog": "tl", "tajik": "tg", "tamil": "ta", "tatar": "tt", "telugu": "te", "thai": "th", "tibetan": "bo",
	"turkish": "tr", "turkmen": "tk", "ukrainian": "uk", "urdu": "ur", "uzbek": "uz", "vietnamese": "vi",
	"welsh": "cy", "yiddish": "yi", "yoruba": "yo",
}

// supportedLanguages is the set of ISO codes Whisper accepts.
var supportedLanguages = func() map[string]bool {
	codes := make(map[string]bool, len(whisperLanguages))
	for _, code := range whisperLanguages {
		codes[code] = true
	}

	return codes
}()

// normalizeLanguage turns a requested code ("RU", " ru ") into the form Whisper accepts.
func normalizeLanguage(code string) (string, bool) {
	code = strings.ToLower(strings.TrimSpace(code))

	return code, supportedLanguages[code]
}

// isoLanguage converts what Whisper reports ("russian", or already a code) to an ISO code;
// empty if unknown.
func isoLanguage(reported string) string {
	reported = strings.ToLower(strings.TrimSpace(reported))
	if supportedLanguages[reported] {
		return reported
	}

	return whisperLanguages[reported]
}
