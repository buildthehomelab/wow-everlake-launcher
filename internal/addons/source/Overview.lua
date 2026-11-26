-- The Everlake Professions window: it takes the place of the client's Professions page (ProfessionsBookFrame).
--
-- The client's page has room for the primary professions in its own slots (one on Everlake, which keeps the second
-- slot free so every profession can be learned). This window, in the client's style, lists every known primary
-- profession in a scrolling list (icon, rank bar, spells, unlearn X) above the client's secondary professions.
-- Whenever the client opens its page (side tab, key binding, anything), it is closed and this window opens in its
-- place. In combat the client's page stays: secure buttons cannot be shown or hidden then.
local _, addon = ...
local P = addon.Professions
if not P then return end

local GATHER_MAIN = { [186] = 2580, [182] = 2383 }                -- Mining: Find Minerals, Herbalism: Find Herbs
local EXTRA_SPELLS = { [333] = { 13262 }, [186] = { 2656 }, [755] = { 31252 }, [773] = { 51005 } }   -- Disenchant, Smelting, Prospecting, Milling
local BAR_COLORS = {
    [171] = { 0.25, 0.75, 0.3 }, [164] = { 0.85, 0.35, 0.1 }, [333] = { 0.6, 0.35, 0.9 }, [202] = { 0.9, 0.7, 0.2 },
    [773] = { 0.3, 0.55, 0.9 }, [755] = { 0.2, 0.75, 0.8 }, [165] = { 0.65, 0.45, 0.2 }, [197] = { 0.8, 0.3, 0.6 },
    [186] = { 0.6, 0.6, 0.7 }, [182] = { 0.35, 0.7, 0.2 }, [393] = { 0.7, 0.5, 0.3 },
    [185] = { 0.85, 0.8, 0.65 }, [356] = { 0.3, 0.45, 0.85 }, [129] = { 0.45, 0.85, 0.1 },
}

local function RankTitle(maxRank)
    if not maxRank or maxRank <= 0 then return "" end
    if maxRank <= 75 then return "Apprentice" elseif maxRank <= 150 then return "Journeyman"
    elseif maxRank <= 225 then return "Expert" elseif maxRank <= 300 then return "Artisan" end
    return "Master"
end

local function IsPassive(id)
    if C_Spell and C_Spell.IsSpellPassive then return C_Spell.IsSpellPassive(id) end
    return IsPassiveSpell and IsPassiveSpell(id)
end

-- The spells the client's own page shows for a profession it knows (GetProfessions index): its spellbook entries.
local function BookSpells(index)
    local _, _, _, _, numSpells, offset = GetProfessionInfo(index)
    local ids = {}
    for i = 1, numSpells or 0 do
        local slot, id = (offset or 0) + i, nil
        if C_SpellBook and C_SpellBook.GetSpellBookItemInfo and Enum and Enum.SpellBookSpellBank then
            local info = C_SpellBook.GetSpellBookItemInfo(slot, Enum.SpellBookSpellBank.Player)
            id = info and (info.spellID or info.actionID)
            if info and info.isPassive then id = nil end
        elseif GetSpellBookItemInfo then
            local _, spellId = GetSpellBookItemInfo(slot, BOOKTYPE_PROFESSION or "spell")
            id = spellId
        end
        if id and not IsPassive(id) then ids[#ids + 1] = id end
    end
    return ids
end

-- The client's professions: primaries by skill line, and its secondary ones in its order.
local function ClientProfessions()
    local primary, secondary = {}, {}
    if not GetProfessions or not GetProfessionInfo then return primary, secondary end
    local t = { GetProfessions() }
    for k = 1, 6 do
        local index = t[k]
        if index then
            local name, icon, rank, maxRank, _, _, skillLine = GetProfessionInfo(index)
            local entry = { index = index, name = name, icon = icon, rank = rank, maxRank = maxRank, skill = skillLine }
            if k <= 2 then
                if skillLine then primary[skillLine] = entry end
            elseif name then
                secondary[#secondary + 1] = entry
            end
        end
    end
    return primary, secondary
end

-- spells of a primary profession the client has no slot for: its own spell (or the gathering tracker), then extras
local function StaticSpells(entry)
    local spells = {}
    local main = GATHER_MAIN[entry.skill] or entry.castId
    if main and P.Knows(main) then spells[#spells + 1] = main end
    for _, id in ipairs(EXTRA_SPELLS[entry.skill] or {}) do
        if P.Knows(id) then spells[#spells + 1] = id end
    end
    return spells
end

-- Unlearn, as the client's own red X: a confirmation, then AbandonSkill(skill line). The server unlearns any primary
-- profession by skill line (CMSG_UNLEARN_SKILL), in the client's slots or not. Only a new entry in the client's dialog
-- table: assigning the global itself would taint every popup.
if StaticPopupDialogs then
    StaticPopupDialogs.EVERLAKE_UNLEARN_PROFESSION = {
        text = UNLEARN_SKILL or "Do you really want to unlearn %s?",
        button1 = UNLEARN or ACCEPT or "Unlearn",
        button2 = CANCEL or "Cancel",
        OnAccept = function(_, skill) if skill and AbandonSkill then AbandonSkill(skill) end end,
        timeout = 0,
        whileDead = 1,
        hideOnEscape = 1,
        showAlert = 1,
    }
end

local function TryCreate(frameType, name, parent, ...)
    for _, template in ipairs({ ... }) do
        local ok, f = pcall(CreateFrame, frameType, name, parent, template)
        if ok and f then return f, template end
    end
    return CreateFrame(frameType, name, parent)
end

local function Panel(parent)
    local f, template = TryCreate("Frame", nil, parent, "InsetFrameTemplate")
    if not template then
        local bg = f:CreateTexture(nil, "BACKGROUND")
        bg:SetAllPoints()
        bg:SetColorTexture(0, 0, 0, 0.35)
    end
    return f
end

local function SpellButton(parent)
    local b = CreateFrame("Button", nil, parent, "SecureActionButtonTemplate")
    b:SetSize(40, 40)
    b:RegisterForClicks("AnyUp", "AnyDown")
    b.back = b:CreateTexture(nil, "BACKGROUND")
    b.back:SetPoint("TOPLEFT", -2, 2)
    b.back:SetPoint("BOTTOMRIGHT", 2, -2)
    b.back:SetColorTexture(0, 0, 0, 0.9)
    b.icon = b:CreateTexture(nil, "ARTWORK")
    b.icon:SetAllPoints()
    b.icon:SetTexCoord(0.07, 0.93, 0.07, 0.93)
    b:SetHighlightTexture("Interface\\Buttons\\ButtonHilight-Square", "ADD")
    b.label = b:CreateFontString(nil, "OVERLAY", "GameFontNormal")
    b.label:SetPoint("TOPLEFT", b, "TOPRIGHT", 8, -4)
    b.sub = b:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    b.sub:SetPoint("TOPLEFT", b.label, "BOTTOMLEFT", 0, -2)
    b:SetScript("OnEnter", function(self)
        if self.spellId then
            GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
            if GameTooltip.SetSpellByID then GameTooltip:SetSpellByID(self.spellId) else GameTooltip:SetText(self.label:GetText() or "") end
            GameTooltip:Show()
        end
    end)
    b:SetScript("OnLeave", function() GameTooltip:Hide() end)
    return b
end

local function SetSpell(b, id, sub)
    local name, icon = P.SpellNameIcon(id)
    b.spellId = id
    b.icon:SetTexture(icon)
    b.label:SetText(name or "")
    b.sub:SetText(sub or "")
    b:SetAttribute("type", "spell")
    b:SetAttribute("spell", name)
    b:Show()
end

local function Bar(parent)
    local bar = CreateFrame("StatusBar", nil, parent)
    bar:SetHeight(20)
    bar:SetStatusBarTexture("Interface\\TargetingFrame\\UI-StatusBar")
    bar.bg = bar:CreateTexture(nil, "BACKGROUND")
    bar.bg:SetPoint("TOPLEFT", -1, 1)
    bar.bg:SetPoint("BOTTOMRIGHT", 1, -1)
    bar.bg:SetColorTexture(0, 0, 0, 0.9)
    bar.text = bar:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    bar.text:SetPoint("CENTER")
    return bar
end

local function SetBar(bar, skill, name, rank, maxRank)
    if rank and maxRank and maxRank > 0 then
        local c = BAR_COLORS[skill] or { 0.9, 0.6, 0.1 }
        bar:SetStatusBarColor(c[1], c[2], c[3])
        bar:SetMinMaxValues(0, maxRank)
        bar:SetValue(rank)
        bar.text:SetText(string.format("%s %d/%d", name or "", rank, maxRank))
        bar:Show()
    else
        bar:Hide()
    end
end

local function UnlearnButton(parent)
    local b = CreateFrame("Button", nil, parent)
    b:SetSize(24, 24)
    b:SetNormalTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Up")
    b:SetPushedTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Down")
    b:SetHighlightTexture("Interface\\Buttons\\UI-GroupLoot-Pass-Highlight", "ADD")
    b:SetScript("OnClick", function(self)
        if self.skill then StaticPopup_Show("EVERLAKE_UNLEARN_PROFESSION", self.profession, nil, self.skill) end
    end)
    b:SetScript("OnEnter", function(self)
        GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
        GameTooltip:SetText(UNLEARN or "Unlearn")
        GameTooltip:Show()
    end)
    b:SetScript("OnLeave", function() GameTooltip:Hide() end)
    return b
end

local window, list, content
local sections, secondaries = {}, {}
local SECTION_H, SECTION_GAP, BOTTOM_H, SPELL_STEP = 150, 6, 330, 46

local function PrimarySection(i, width)
    local f = sections[i]
    if not f then
        f = Panel(content)
        f.header = f:CreateFontString(nil, "OVERLAY", "GameFontNormal")
        f.header:SetPoint("TOPLEFT", 16, -12)
        f.spells = {}
        f.bar = Bar(f)
        f.unlearn = UnlearnButton(f)
        f.unlearn:SetPoint("LEFT", f.bar, "RIGHT", 6, 0)
        sections[i] = f
    end
    f:SetSize(width, SECTION_H)
    f:ClearAllPoints()
    f:SetPoint("TOPLEFT", 0, -(i - 1) * (SECTION_H + SECTION_GAP))
    f.bar:ClearAllPoints()
    f.bar:SetPoint("TOPLEFT", f, "TOPLEFT", math.floor(width * 0.36), -48)
    f.bar:SetPoint("RIGHT", f, "RIGHT", -46, 0)
    return f
end

local function FillPrimary(f, entry, spells)
    f.header:SetText(entry.name)
    for k = 1, math.max(#spells, #f.spells) do
        local b = f.spells[k]
        if spells[k] then
            if not b then
                b = SpellButton(f)
                b:SetPoint("TOPLEFT", 14, -36 - (k - 1) * SPELL_STEP)
                f.spells[k] = b
            end
            SetSpell(b, spells[k], k == 1 and RankTitle(entry.maxRank) or nil)
        elseif b then
            b:Hide()
        end
    end
    if not spells[1] then   -- nothing to cast (Skinning): the profession's icon and name
        local b = f.spells[1]
        if not b then
            b = SpellButton(f)
            b:SetPoint("TOPLEFT", 14, -36)
            f.spells[1] = b
        end
        b.spellId = nil
        b:SetAttribute("type", nil)
        b.icon:SetTexture(entry.icon)
        b.label:SetText(entry.name)
        b.sub:SetText(RankTitle(entry.maxRank))
        b:Show()
    end
    SetBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank)
    f.unlearn.skill, f.unlearn.profession = entry.skill, entry.name
    f.unlearn:SetShown(f.bar:IsShown())
    local height = math.max(SECTION_H, 36 + math.max(1, #spells) * SPELL_STEP + 4)
    f:SetHeight(height)
    f:Show()
    return height
end

local function SecondaryPanel(i)
    local f = secondaries[i]
    if f then return f end
    f = Panel(window)
    f.title = f:CreateFontString(nil, "OVERLAY", "GameFontNormal")
    f.title:SetPoint("TOP", 0, -14)
    f.bar = Bar(f)
    f.bar:SetPoint("TOPLEFT", 14, -36)
    f.bar:SetPoint("TOPRIGHT", -14, -36)
    f.spells = {}
    secondaries[i] = f
    return f
end

local function FillSecondary(f, entry, spells)
    f.title:SetText(entry.name)
    SetBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank)
    -- spells stacked from the bottom, as the client's page shows them
    for k = 1, math.max(#spells, #f.spells) do
        local b = f.spells[k]
        if spells[k] then
            if not b then b = SpellButton(f) f.spells[k] = b end
            b:ClearAllPoints()
            b:SetPoint("BOTTOMLEFT", 14, 14 + (#spells - k) * SPELL_STEP)
            SetSpell(b, spells[k], k == 1 and RankTitle(entry.maxRank) or nil)
        elseif b then
            b:Hide()
        end
    end
    f:Show()
end

local pending
local function Refresh()
    if not window then return end
    if InCombatLockdown() then pending = true return end
    pending = false
    local clientPrimary, clientSecondary = ClientProfessions()

    local width = list:GetWidth()
    if not width or width < 100 then width = (window:GetWidth() or 700) - 60 end
    local y, n = 0, 0
    for _, entry in ipairs(P.Known()) do
        n = n + 1
        local client = clientPrimary[entry.skill]
        local spells = client and BookSpells(client.index) or StaticSpells(entry)
        if client and #spells == 0 then spells = StaticSpells(entry) end
        local f = PrimarySection(n, width)
        f:ClearAllPoints()
        f:SetPoint("TOPLEFT", 0, -y)
        y = y + FillPrimary(f, entry, spells) + SECTION_GAP
    end
    for i = n + 1, #sections do sections[i]:Hide() end
    content:SetSize(width, math.max(1, y))
    window.empty:SetShown(n == 0)

    local count = #clientSecondary
    local total = (window:GetWidth() or 700) - 24
    local w = count > 0 and math.floor((total - (count - 1) * 6) / count) or total
    for i, entry in ipairs(clientSecondary) do
        local f = SecondaryPanel(i)
        f:ClearAllPoints()
        f:SetSize(w, BOTTOM_H)
        f:SetPoint("BOTTOMLEFT", window, "BOTTOMLEFT", 12 + (i - 1) * (w + 6), 12)
        FillSecondary(f, entry, BookSpells(entry.index))
    end
    for i = count + 1, #secondaries do secondaries[i]:Hide() end
end

local function CreateWindow(book)
    window = TryCreate("Frame", "EverlakeProfessionsBook", UIParent, "PortraitFrameTemplate", "ButtonFrameTemplate", "BasicFrameTemplateWithInset")
    window:SetAllPoints(book)   -- the client's page's place and size
    window:SetFrameStrata(book:GetFrameStrata())
    window:SetToplevel(true)
    window:EnableMouse(true)
    window:Hide()
    -- title and the client page's portrait; guarded, as templates name these parts differently between clients
    pcall(function()
        local title = PROFESSIONS_BUTTON or TRADE_SKILLS or "Professions"
        if window.SetTitle then window:SetTitle(title)
        elseif type(window.TitleText) == "table" then window.TitleText:SetText(title)
        elseif type(window.TitleContainer) == "table" and type(window.TitleContainer.TitleText) == "table" then
            window.TitleContainer.TitleText:SetText(title)
        end
    end)
    pcall(function()
        local portrait = (type(book.PortraitContainer) == "table" and book.PortraitContainer.portrait)
            or (type(book.portrait) == "table" and book.portrait) or _G.ProfessionsBookFramePortrait
        local texture = type(portrait) == "table" and portrait.GetTexture and portrait:GetTexture()
        if texture then
            if window.SetPortraitToAsset then window:SetPortraitToAsset(texture)
            elseif window.SetPortraitTextureRaw then window:SetPortraitTextureRaw(texture) end
        end
    end)
    if UISpecialFrames then table.insert(UISpecialFrames, "EverlakeProfessionsBook") end   -- Escape closes it

    local top = Panel(window)
    top:SetPoint("TOPLEFT", 12, -30)
    top:SetPoint("TOPRIGHT", -12, -30)
    top:SetPoint("BOTTOM", window, "BOTTOM", 0, 12 + BOTTOM_H + 6)
    list = TryCreate("ScrollFrame", "EverlakeProfessionsBookList", top, "UIPanelScrollFrameTemplate")
    list:SetPoint("TOPLEFT", 6, -6)
    list:SetPoint("BOTTOMRIGHT", -28, 6)
    list:EnableMouseWheel(true)
    list:SetScript("OnMouseWheel", function(self, delta)
        local range = self:GetVerticalScrollRange() or 0
        self:SetVerticalScroll(math.max(0, math.min(range, (self:GetVerticalScroll() or 0) - delta * 60)))
    end)
    content = CreateFrame("Frame", nil, list)
    list:SetScrollChild(content)
    window.empty = top:CreateFontString(nil, "OVERLAY", "GameFontDisable")
    window.empty:SetPoint("CENTER")
    window.empty:SetText("No primary professions learned yet.")
end

-- the client's page opens: close it, open ours in its place (or close ours when it was open: the key toggles)
local function TakeOver(book)
    if InCombatLockdown() then return end
    if not window then CreateWindow(book) end
    if window:IsShown() then
        book:Hide()
        window:Hide()
        return
    end
    book:Hide()
    window:Show()
    Refresh()
    if C_Timer and C_Timer.After then C_Timer.After(0, Refresh) end   -- once the list has its size
end

local hooked
local function Hook()
    local book = _G.ProfessionsBookFrame
    if not book or hooked then return end
    hooked = true
    book:HookScript("OnShow", TakeOver)
    if book:IsShown() then TakeOver(book) end
end

local events = CreateFrame("Frame")
for _, e in ipairs({ "PLAYER_LOGIN", "ADDON_LOADED", "SKILL_LINES_CHANGED", "SPELLS_CHANGED", "LEARNED_SPELL_IN_TAB", "PLAYER_REGEN_ENABLED" }) do
    pcall(events.RegisterEvent, events, e)
end
events:SetScript("OnEvent", function(_, event)
    Hook()
    if window and window:IsShown() and (event ~= "PLAYER_REGEN_ENABLED" or pending) then Refresh() end
end)

addon.OverviewWindow = function() return window end
