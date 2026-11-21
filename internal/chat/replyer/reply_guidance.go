package replyer

var expressionIntentTemplates = map[string]map[string]string{
	"zh-CN": {
		"reply_act": "你的回复目的是{value}。",
		"focus":     "表达重点是{value}，",
		"tone":      "可以用{value}的语气表达。",
	},
	"en-US": {
		"reply_act": "The purpose of your reply is {value}. ",
		"focus":     "Focus on {value}. ",
		"tone":      "You may use a {value} tone.",
	},
	"ja-JP": {
		"reply_act": "あなたの返信の目的は{value}です。",
		"focus":     "伝える重点は{value}です。",
		"tone":      "{value}という口調で表現できます。",
	},
}

var groupReplyGuidance = map[string]map[string]string{
	"zh-CN": {
		"简短表达": "你正在群里聊天,现在请你读读之前的聊天记录，然后给出日常且口语化的回复。\n请简短的回复，符合口语习惯，1-2句短句即可。不要长篇大论，无复杂修辞和比喻。",
		"正常回复": "你正在群里聊天,现在请你读读之前的聊天记录，把握当前的话题，然后给出日常且口语化的回复。\n最好一次对一个话题进行回复，免得啰嗦或者回复内容太乱。",
		"长回复":  "你正在群里聊天,现在请你读读之前的聊天记录，把握当前的话题，给出回复。\n可以针对问题做出较为详细的评论和说明。",
	},
	"en-US": {
		"简短表达": "You are chatting in a group. Read the chat log above and reply naturally in a conversational tone.\nKeep your reply to one or two short, conversational sentences. Avoid long explanations, elaborate rhetoric, and metaphors.",
		"正常回复": "You are chatting in a group. Read the chat log above, follow the current topic, and reply naturally in a conversational tone.\nTry to reply to one topic at a time, so that your reply does not get wordy or messy.",
		"长回复":  "You are chatting in a group. Read the chat log above, follow the current topic, and reply.\nYou may comment on and explain the question in detail.",
	},
	"ja-JP": {
		"简短表达": "あなたはグループでチャットしています。上の履歴を読み、日常的な話し言葉で返信してください。\n口語的な短い一文か二文で簡潔に返信してください。長々と説明せず、複雑な修辞や比喩は使わないでください。",
		"正常回复": "あなたはグループでチャットしています。上の履歴を読み、今の話題を踏まえて、日常的な話し言葉で返信してください。\n一度にひとつの話題へ返信すると、くどくなったり内容が乱れたりしません。",
		"长回复":  "あなたはグループでチャットしています。上の履歴を読み、今の話題を踏まえて返信してください。\n質問について詳しく意見を述べたり説明したりして構いません。",
	},
}

var privateReplyGuidance = map[string]map[string]string{
	"zh-CN": {
		"简短表达": "你正在和对方私聊,现在请你读读之前的聊天记录，然后给出日常且口语化的回复。\n请简短的回复，符合口语习惯，1-2句短句即可。不要长篇大论，无复杂修辞和比喻。",
		"正常回复": "你正在和对方私聊,现在请你读读之前的聊天记录，把握当前的话题，然后给出日常且口语化的回复。\n最好一次对一个话题进行回复，免得啰嗦或者回复内容太乱。",
		"长回复":  "你正在和对方私聊,现在请你读读之前的聊天记录，把握当前的话题，给出回复。\n可以针对问题做出较为详细的评论和说明。",
	},
	"en-US": {
		"简短表达": "You are chatting privately with someone. Read the chat log above and reply naturally in a conversational tone.\nKeep your reply to one or two short, conversational sentences. Avoid long explanations, elaborate rhetoric, and metaphors.",
		"正常回复": "You are chatting privately with someone. Read the chat log above, follow the current topic, and reply naturally in a conversational tone.\nTry to reply to one topic at a time, so that your reply does not get wordy or messy.",
		"长回复":  "You are chatting privately with someone. Read the chat log above, follow the current topic, and reply.\nYou may comment on and explain the question in detail.",
	},
	"ja-JP": {
		"简短表达": "あなたは相手と個別チャットしています。上の履歴を読み、日常的な話し言葉で返信してください。\n口語的な短い一文か二文で簡潔に返信してください。長々と説明せず、複雑な修辞や比喩は使わないでください。",
		"正常回复": "あなたは相手と個別チャットしています。上の履歴を読み、今の話題を踏まえて、日常的な話し言葉で返信してください。\n一度にひとつの話題へ返信すると、くどくなったり内容が乱れたりしません。",
		"长回复":  "あなたは相手と個別チャットしています。上の履歴を読み、今の話題を踏まえて返信してください。\n質問について詳しく意見を述べたり説明したりして構いません。",
	},
}
