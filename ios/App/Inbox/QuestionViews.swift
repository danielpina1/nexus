import NexusCore
import SwiftUI

/// Agent question (`ask_user`, Claude Code's AskUserQuestion): header, option cards with
/// descriptions, multi-select and "Other…". info/35 border until answered. Used in the Inbox,
/// on Home and in team runs.
struct QuestionCardView: View {
    let question: QuestionItem
    var compact = false

    @Environment(NexusStore.self) private var store

    var body: some View {
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) { meta }
                VStack(alignment: .leading, spacing: 4) { meta }
            }
            Text(question.question.text)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            QuestionOptionsView(taskID: question.taskID, question: question.question, answered: store.questions[question.id] == nil)
            if !compact && !question.taskID.isEmpty {
                NavigationLink(value: TaskRoute(id: question.taskID)) {
                    Text(store.tasks[question.taskID]?.displayTitle ?? "Open task")
                        .lineLimit(1)
                }
                .buttonStyle(NXButtonStyle(kind: .ghost, size: .small))
            }
        }
        .nxCard(urgency: .info)
        .accessibilityElement(children: .contain)
    }

    @ViewBuilder
    private var meta: some View {
        HStack(spacing: 6) {
            NXBadge("Question", systemImage: "questionmark.bubble", tone: .info)
            if !question.question.header.isEmpty {
                NXBadge(question.question.header, tone: .info)
            }
        }
        Text(Format.relative(question.at))
            .nx(NXType.caption.weight(.regular))
            .foregroundStyle(NXColor.muted)
    }
}

/// A question inside the chat bubble (same answer UI as the Inbox card).
struct ChatQuestionCard: View {
    let taskID: String
    let question: Nexus_V1_Question

    @Environment(NexusStore.self) private var store

    var body: some View {
        let answered = store.questions[question.questionID] == nil
        VStack(alignment: .leading, spacing: NXSpace.s2) {
            HStack(spacing: 6) {
                Image(systemName: "questionmark.bubble")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(NXColor.info)
                    .accessibilityHidden(true)
                if !question.header.isEmpty {
                    NXBadge(question.header, tone: .info)
                }
            }
            Text(question.text)
                .nx(NXType.body.weight(.semibold))
                .foregroundStyle(NXColor.fg)
                .fixedSize(horizontal: false, vertical: true)
            QuestionOptionsView(taskID: taskID, question: question, answered: answered)
        }
        .nxCard(urgency: answered ? nil : .info)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Question: \(question.text)")
    }
}

/// The answer UI of a question (web QuestionOptions): option cards (label + description), tap to
/// answer; multi-select with a Submit button (labels joined with ", "); "Other…" free text.
struct QuestionOptionsView: View {
    let taskID: String
    let question: Nexus_V1_Question
    let answered: Bool

    @Environment(NexusStore.self) private var store
    @Environment(ToastCenter.self) private var toasts
    @State private var picked: [String] = []
    @State private var showOther = false
    @State private var otherText = ""
    @State private var sending = false
    @State private var answer: String?
    @State private var errorMessage: String?
    @FocusState private var otherFocused: Bool

    private struct Option: Identifiable {
        var id: String { label }
        let label: String
        let description: String
    }

    private var options: [Option] {
        if !question.options.isEmpty {
            return question.options.map { Option(label: $0.label, description: $0.description_p) }
        }
        return question.choices.map { Option(label: $0, description: "") }
    }

    private var allowsOther: Bool { question.allowFreeText || options.isEmpty }

    var body: some View {
        if answered || answer != nil {
            HStack(spacing: NXSpace.s2) {
                NXBadge("Answered", systemImage: "checkmark", tone: .ok)
                if let answer {
                    Text(answer)
                        .nx(NXType.footnote)
                        .foregroundStyle(NXColor.fg2)
                        .lineLimit(2)
                }
            }
            .accessibilityElement(children: .combine)
        } else {
            VStack(alignment: .leading, spacing: NXSpace.s2) {
                ForEach(options) { option in
                    optionButton(option)
                }
                if allowsOther {
                    if showOther || options.isEmpty {
                        HStack(spacing: NXSpace.s2) {
                            TextField("Type your answer…", text: $otherText, axis: .vertical)
                                .lineLimit(1...4)
                                .nxText(NXType.bodyLg)
                                .foregroundStyle(NXColor.fg)
                                .focused($otherFocused)
                                .submitLabel(.send)
                                .onSubmit { submitOther() }
                                .padding(.horizontal, NXSpace.s3)
                                .padding(.vertical, 10)
                                .frame(minHeight: NXSize.touchMin)
                                .background(NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
                                .overlay {
                                    RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                                        .strokeBorder(otherFocused ? NXColor.ring : NXColor.borderInput, lineWidth: otherFocused ? 2 : 1)
                                }
                                .accessibilityLabel("Your answer")
                            NXIconButton(systemImage: "paperplane.fill", label: "Send answer", tint: NXColor.accent, isLoading: sending) {
                                submitOther()
                            }
                            .disabled(otherText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || sending)
                        }
                    } else {
                        Button {
                            showOther = true
                            otherFocused = true
                        } label: {
                            Text("Other…")
                                .nx(NXType.footnote)
                                .foregroundStyle(NXColor.fg2)
                                .frame(maxWidth: .infinity, minHeight: NXSize.touchMin, alignment: .leading)
                                .padding(.horizontal, NXSpace.s3)
                                .overlay {
                                    RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                                        .strokeBorder(NXColor.border, style: StrokeStyle(lineWidth: 1, dash: [4, 3]))
                                }
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(NXPlainPressStyle())
                        .accessibilityHint("Type your own answer.")
                    }
                }
                if question.multiSelect {
                    NXButton(picked.isEmpty ? "Submit" : "Submit (\(picked.count))", systemImage: "checkmark", kind: .primary, size: .small, isLoading: sending) {
                        submit(choice: picked.joined(separator: ", "), text: "")
                    }
                    .disabled(picked.isEmpty)
                }
                if let errorMessage {
                    NXInlineError(errorMessage) { self.errorMessage = nil }
                }
            }
        }
    }

    private func optionButton(_ option: Option) -> some View {
        let on = picked.contains(option.label)
        return Button {
            if question.multiSelect {
                if on { picked.removeAll { $0 == option.label } } else { picked.append(option.label) }
            } else {
                submit(choice: option.label, text: "")
            }
        } label: {
            HStack(alignment: .top, spacing: NXSpace.s2) {
                if question.multiSelect {
                    Image(systemName: on ? "checkmark.square.fill" : "square")
                        .font(.system(size: 16, weight: .medium))
                        .foregroundStyle(on ? NXColor.accent : NXColor.borderStrong)
                        .accessibilityHidden(true)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(option.label)
                        .nx(NXType.body.weight(.medium))
                        .foregroundStyle(NXColor.fg)
                    if !option.description.isEmpty {
                        Text(option.description)
                            .nx(NXType.footnote)
                            .foregroundStyle(NXColor.fg2)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, NXSpace.s3)
            .padding(.vertical, NXSpace.s2)
            .frame(maxWidth: .infinity, minHeight: NXSize.touchMin, alignment: .leading)
            .background(on ? NXColor.accentSoft : NXColor.surface, in: RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: NXRadius.md, style: .continuous)
                    .strokeBorder(on ? NXColor.accent : NXColor.border, lineWidth: 1)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(NXPlainPressStyle())
        .disabled(sending)
        .accessibilityLabel(option.description.isEmpty ? option.label : "\(option.label), \(option.description)")
        .accessibilityAddTraits(question.multiSelect && on ? .isSelected : [])
        .accessibilityHint(question.multiSelect ? "Selects this option." : "Answers with this option.")
    }

    private func submitOther() {
        let text = otherText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        submit(choice: question.multiSelect ? picked.joined(separator: ", ") : "", text: text)
    }

    /// `choice`: the option label(s); `text`: a free-text answer (wins when both are set).
    private func submit(choice: String, text: String) {
        guard !sending else { return }
        let shown = text.isEmpty ? choice : (choice.isEmpty ? text : "\(choice), \(text)")
        sending = true
        errorMessage = nil
        Task {
            defer { sending = false }
            do {
                let delivery = try await store.sendMessage(
                    taskID: taskID,
                    text: text.isEmpty ? choice : text,
                    questionID: question.questionID,
                    choice: choice.isEmpty ? nil : choice
                )
                answer = shown
                otherText = ""
                if delivery.isQueued {
                    toasts.info("Queued", "Your answer is sent when your Nexus is reachable.")
                }
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }
}
