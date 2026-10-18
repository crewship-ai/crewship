// Browser component fixture: synthetic identity and send sink, no model/server.
import React, { useState } from "react"
import { createRoot } from "react-dom/client"
import { ChatComposer } from "../components/features/chat/composer/chat-composer"

function Fixture() {
  const [enabled, setEnabled] = useState(true)
  const [refuse, setRefuse] = useState(false)
  const [messages, setMessages] = useState<unknown[]>([])
  return <main>
    <button onClick={() => setEnabled(value => !value)}>Toggle availability</button>
    <button onClick={() => setRefuse(value => !value)}>Toggle send refusal</button>
    <ChatComposer agentId="fixture-agent" sessionId="fixture-chat"
      variant={innerWidth < 768 ? "mobile" : "desktop"} isStreaming={false}
      connectionStatus="connected" documentCreationEnabled={enabled}
      ensureSession={async () => true} stopGeneration={() => {}}
      sendMessage={(text, metadata) => { setMessages(prior => [...prior, { text, metadata }]); return !refuse }} />
    <output aria-label="Sent fixture messages">{JSON.stringify(messages)}</output>
  </main>
}
createRoot(document.getElementById("root")!).render(<Fixture />)
