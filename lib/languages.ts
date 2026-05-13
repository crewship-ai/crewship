/** A supported language with ISO code, flag emoji, and localized name. */
export interface Language {
  code: string
  flag: string
  name: string
  native: string
}

/** All supported languages for agent system prompt localization. */
export const LANGUAGES: Language[] = [
  { code: "af", flag: "\u{1F1FF}\u{1F1E6}", name: "Afrikaans", native: "Afrikaans" },
  { code: "ar", flag: "\u{1F1F8}\u{1F1E6}", name: "Arabic", native: "\u0627\u0644\u0639\u0631\u0628\u064A\u0629" },
  { code: "bg", flag: "\u{1F1E7}\u{1F1EC}", name: "Bulgarian", native: "\u0411\u044A\u043B\u0433\u0430\u0440\u0441\u043A\u0438" },
  { code: "bn", flag: "\u{1F1E7}\u{1F1E9}", name: "Bengali", native: "\u09AC\u09BE\u0982\u09B2\u09BE" },
  { code: "ca", flag: "\u{1F1EA}\u{1F1F8}", name: "Catalan", native: "Catal\u00E0" },
  { code: "cs", flag: "\u{1F1E8}\u{1F1FF}", name: "Czech", native: "\u010Ce\u0161tina" },
  { code: "da", flag: "\u{1F1E9}\u{1F1F0}", name: "Danish", native: "Dansk" },
  { code: "de", flag: "\u{1F1E9}\u{1F1EA}", name: "German", native: "Deutsch" },
  { code: "el", flag: "\u{1F1EC}\u{1F1F7}", name: "Greek", native: "\u0395\u03BB\u03BB\u03B7\u03BD\u03B9\u03BA\u03AC" },
  { code: "en", flag: "\u{1F1EC}\u{1F1E7}", name: "English", native: "English" },
  { code: "es", flag: "\u{1F1EA}\u{1F1F8}", name: "Spanish", native: "Espa\u00F1ol" },
  { code: "et", flag: "\u{1F1EA}\u{1F1EA}", name: "Estonian", native: "Eesti" },
  { code: "fa", flag: "\u{1F1EE}\u{1F1F7}", name: "Persian", native: "\u0641\u0627\u0631\u0633\u06CC" },
  { code: "fi", flag: "\u{1F1EB}\u{1F1EE}", name: "Finnish", native: "Suomi" },
  { code: "fr", flag: "\u{1F1EB}\u{1F1F7}", name: "French", native: "Fran\u00E7ais" },
  { code: "he", flag: "\u{1F1EE}\u{1F1F1}", name: "Hebrew", native: "\u05E2\u05D1\u05E8\u05D9\u05EA" },
  { code: "hi", flag: "\u{1F1EE}\u{1F1F3}", name: "Hindi", native: "\u0939\u093F\u0928\u094D\u0926\u0940" },
  { code: "hr", flag: "\u{1F1ED}\u{1F1F7}", name: "Croatian", native: "Hrvatski" },
  { code: "hu", flag: "\u{1F1ED}\u{1F1FA}", name: "Hungarian", native: "Magyar" },
  { code: "id", flag: "\u{1F1EE}\u{1F1E9}", name: "Indonesian", native: "Bahasa Indonesia" },
  { code: "it", flag: "\u{1F1EE}\u{1F1F9}", name: "Italian", native: "Italiano" },
  { code: "ja", flag: "\u{1F1EF}\u{1F1F5}", name: "Japanese", native: "\u65E5\u672C\u8A9E" },
  { code: "ko", flag: "\u{1F1F0}\u{1F1F7}", name: "Korean", native: "\uD55C\uAD6D\uC5B4" },
  { code: "lt", flag: "\u{1F1F1}\u{1F1F9}", name: "Lithuanian", native: "Lietuvi\u0173" },
  { code: "lv", flag: "\u{1F1F1}\u{1F1FB}", name: "Latvian", native: "Latvie\u0161u" },
  { code: "ms", flag: "\u{1F1F2}\u{1F1FE}", name: "Malay", native: "Bahasa Melayu" },
  { code: "nb", flag: "\u{1F1F3}\u{1F1F4}", name: "Norwegian", native: "Norsk" },
  { code: "nl", flag: "\u{1F1F3}\u{1F1F1}", name: "Dutch", native: "Nederlands" },
  { code: "pl", flag: "\u{1F1F5}\u{1F1F1}", name: "Polish", native: "Polski" },
  { code: "pt", flag: "\u{1F1F5}\u{1F1F9}", name: "Portuguese", native: "Portugu\u00EAs" },
  { code: "pt-BR", flag: "\u{1F1E7}\u{1F1F7}", name: "Portuguese (Brazil)", native: "Portugu\u00EAs (Brasil)" },
  { code: "ro", flag: "\u{1F1F7}\u{1F1F4}", name: "Romanian", native: "Rom\u00E2n\u0103" },
  { code: "ru", flag: "\u{1F1F7}\u{1F1FA}", name: "Russian", native: "\u0420\u0443\u0441\u0441\u043A\u0438\u0439" },
  { code: "sk", flag: "\u{1F1F8}\u{1F1F0}", name: "Slovak", native: "Sloven\u010Dina" },
  { code: "sl", flag: "\u{1F1F8}\u{1F1EE}", name: "Slovenian", native: "Sloven\u0161\u010Dina" },
  { code: "sr", flag: "\u{1F1F7}\u{1F1F8}", name: "Serbian", native: "\u0421\u0440\u043F\u0441\u043A\u0438" },
  { code: "sv", flag: "\u{1F1F8}\u{1F1EA}", name: "Swedish", native: "Svenska" },
  { code: "sw", flag: "\u{1F1F0}\u{1F1EA}", name: "Swahili", native: "Kiswahili" },
  { code: "ta", flag: "\u{1F1EE}\u{1F1F3}", name: "Tamil", native: "\u0BA4\u0BAE\u0BBF\u0BB4\u0BCD" },
  { code: "th", flag: "\u{1F1F9}\u{1F1ED}", name: "Thai", native: "\u0E44\u0E17\u0E22" },
  { code: "tr", flag: "\u{1F1F9}\u{1F1F7}", name: "Turkish", native: "T\u00FCrk\u00E7e" },
  { code: "uk", flag: "\u{1F1FA}\u{1F1E6}", name: "Ukrainian", native: "\u0423\u043A\u0440\u0430\u0457\u043D\u0441\u044C\u043A\u0430" },
  { code: "ur", flag: "\u{1F1F5}\u{1F1F0}", name: "Urdu", native: "\u0627\u0631\u062F\u0648" },
  { code: "vi", flag: "\u{1F1FB}\u{1F1F3}", name: "Vietnamese", native: "Ti\u1EBFng Vi\u1EC7t" },
  { code: "zh", flag: "\u{1F1E8}\u{1F1F3}", name: "Chinese", native: "\u4E2D\u6587" },
  { code: "zh-TW", flag: "\u{1F1F9}\u{1F1FC}", name: "Chinese (Traditional)", native: "\u4E2D\u6587 (\u7E41\u9AD4)" },
]
