import XCTest
@testable import NexusCore
import NexusAPI
import SwiftProtobuf

/// Chat parsing, transcript grouping, model labels, the background-work heuristic and the
/// readable tool renderers (ports of the web's lib/chat.ts, runItems.ts, runMeta.ts,
/// backgroundClaim.ts and tools.ts; keep the vectors in sync with web/e2e/background-claim.test.ts).
final class ChatLogicTests: XCTestCase {
    func testChatCommands() {
        XCTAssertEqual(ChatCommand.parse("/search heat pumps"), .search("heat pumps"))
        XCTAssertEqual(ChatCommand.parse("Remember that the plumber comes"), .remember("the plumber comes"))
        XCTAssertEqual(ChatCommand.parse("@researcher compare pumps"), .mention(target: "researcher", text: "compare pumps"))
        XCTAssertEqual(ChatCommand.parse("@team:home-crew plan it"), .mention(target: "home-crew", text: "plan it"))
        XCTAssertEqual(ChatCommand.parse("remembering stuff"), .message("remembering stuff"))
        XCTAssertEqual(ChatCommand.wireInput("hello", threadAgentID: "code-fixer"), "@code-fixer hello")
        XCTAssertEqual(ChatCommand.wireInput("/search x", threadAgentID: "code-fixer"), "/search x")
        XCTAssertEqual(ChatCommand.wireInput("@team:a b", threadAgentID: nil), "@a b")
        XCTAssertEqual(ChatCommand.wireInput("  hi  ", threadAgentID: nil), "hi")
    }

    func testBackgroundClaims() {
        for text in [
            "Agent is running in the background… I'll report back with a deals table once it finishes.",
            "The subagent is working in the background.",
            "I will get back to you when it's done.",
            "I'll report back shortly.",
        ] {
            XCTAssertTrue(BackgroundClaim.claims(text), text)
        }
        for text in ["Here are the best deals on Worten today.", "I can run tasks in parallel with teams.", "Background: Nexus is a personal assistant."] {
            XCTAssertFalse(BackgroundClaim.claims(text), text)
        }
        let answer = "Agent is running in the background; I'll report back."
        XCTAssertTrue(BackgroundClaim.untracked(answer: answer, taskActive: false, activeChildren: 0))
        XCTAssertFalse(BackgroundClaim.untracked(answer: answer, taskActive: false, activeChildren: 1))
        XCTAssertFalse(BackgroundClaim.untracked(answer: answer, taskActive: true, activeChildren: 0))
        XCTAssertFalse(BackgroundClaim.untracked(answer: "Done.", taskActive: false, activeChildren: 0))
    }

    func testTranscriptGroupsStreamedText() {
        var cost = Nexus_V1_Cost()
        cost.runtime = "claude-code"
        cost.model = "claude-sonnet-4-5-20250929"
        func item(_ id: String, _ kind: TimelineItem.Kind) -> TimelineItem {
            TimelineItem(id: id, at: Date(), runID: "run", kind: kind)
        }
        let entries = [
            item("1", .userMessage("hi")),
            item("2", .cost(cost)),
            item("3", .output("Hel", isFinal: false)),
            item("4", .output("lo", isFinal: false)),
            item("5", .toolCall(Nexus_V1_ToolCall(), result: nil)),
            item("6", .output("World", isFinal: false)),
            item("7", .cost(cost)),
            item("8", .output("", isFinal: true)),
        ]
        let items = ChatItem.build(entries)
        XCTAssertEqual(items.map(\.id), ["3", "5", "6"])
        XCTAssertEqual(ChatItem.replyText(items), "Hello\n\nWorld")
        if case .text(_, let streaming) = items[2].kind { XCTAssertFalse(streaming) } else { XCTFail("text") }
        XCTAssertEqual(RunModel.from(entries, task: nil).label, "Claude Code · claude-sonnet-4-5-20250929")
    }

    func testToolRenderers() throws {
        XCTAssertEqual(ToolText.normalize("mcp__nexus__memory_search"), NormalTool(name: "memory.search", server: ""))
        XCTAssertEqual(ToolText.normalize("mcp__github__create_issue"), NormalTool(name: "create_issue", server: "github"))
        XCTAssertEqual(ToolText.normalize("nexus.memory.search").name, "memory.search")

        let bash = try Google_Protobuf_Struct(jsonString: #"{"command":"go test ./...","description":"Run tests"}"#)
        XCTAssertEqual(ToolText.title("Bash", input: bash).title, "Ran go test ./...")
        XCTAssertEqual(ToolText.detail("Bash", input: bash, cwd: "/src"), .command(command: "go test ./...", cwd: "/src", description: "Run tests"))

        let edit = try Google_Protobuf_Struct(jsonString: #"{"file_path":"/a/README.md","old_string":"a\nrecieve\nc","new_string":"a\nreceive\nc"}"#)
        guard case .edit(let path, let lines) = ToolText.detail("Edit", input: edit) else { return XCTFail("edit") }
        XCTAssertEqual(path, "/a/README.md")
        XCTAssertEqual(lines.map(\.text), ["a", "recieve", "receive", "c"])
        XCTAssertEqual(ToolText.diffStats(lines).added, 1)
        XCTAssertEqual(ToolText.diffStats(lines).removed, 1)

        var approval = Nexus_V1_Approval()
        approval.title = "Assistant wants to use Bash"
        approval.payload = try Google_Protobuf_Struct(jsonString: #"{"tool":"Bash","cwd":"/src","input":{"command":"git push"}}"#)
        XCTAssertEqual(approval.readableTitle, "Assistant wants to: ran git push")
        XCTAssertEqual(approval.readableDetail, .command(command: "git push", cwd: "/src", description: ""))

        var checkout = Nexus_V1_Approval()
        checkout.title = "Checkout €84.30"
        checkout.payload = try Google_Protobuf_Struct(jsonString: #"{"store":"Continente","items":22,"total":"84.30 EUR"}"#)
        XCTAssertEqual(checkout.readableDetail, .spend(amount: "84.30 EUR", merchant: "Continente"))
    }
}
