-- Every primary profession the character knows: tabs down the side of the profession window, and a panel next to the
-- spellbook.
--
-- Everlake lets a character learn all primary professions, but the client's Professions tab has room for two. A
-- profession's window opens from its spell whether or not the tab lists it (/cast Blacksmithing works for all of them),
-- so this panel lists every known profession and casts its spell on click. /everlake prof shows or hides it.
local _, addon = ...
if addon.client ~= "wowf" then return end

-- skill line, the spell ranks that teach it (any one known = learned), and the spell that opens its window
-- (nil = no window: gathering professions with nothing to craft)
local PROFESSIONS = {
    { skill = 171, ranks = { 2259, 3101, 3464, 11611, 28596 }, open = "rank" },          -- Alchemy
    { skill = 164, ranks = { 2018, 3100, 3538, 9785, 29844 }, open = "rank" },           -- Blacksmithing
    { skill = 333, ranks = { 7411, 7412, 7413, 13920, 28029 }, open = "rank" },          -- Enchanting
    { skill = 202, ranks = { 4036, 4037, 4038, 12656, 30350 }, open = "rank" },          -- Engineering
    { skill = 773, ranks = { 45357, 45358, 45359, 45360 }, open = "rank" },              -- Inscription
    { skill = 755, ranks = { 25229, 25230, 28894, 28895 }, open = "rank" },              -- Jewelcrafting
    { skill = 165, ranks = { 2108, 3104, 3811, 10662, 32549 }, open = "rank" },          -- Leatherworking
    { skill = 197, ranks = { 3908, 3909, 3910, 12180, 26790 }, open = "rank" },          -- Tailoring
    { skill = 186, ranks = { 2575, 2576, 3564, 10248, 29354 }, open = 2656 },            -- Mining (Smelting)
    { skill = 182, ranks = { 2366, 2368, 3570, 11993, 28695 } },                         -- Herbalism
    { skill = 393, ranks = { 8613, 8617, 8618, 10768, 32678 } },                         -- Skinning
}

local function SpellNameIcon(id)
    if C_Spell and C_Spell.GetSpellInfo then
        local info = C_Spell.GetSpellInfo(id)
        if info then return info.name, info.iconID end
        return nil
    end
    if GetSpellInfo then
        local name, _, icon = GetSpellInfo(id)
        return name, icon
    end
end

local function Knows(id)
    if IsPlayerSpell then return IsPlayerSpell(id) end
    return IsSpellKnown and IsSpellKnown(id)
end

-- rank and max rank, when the client can tell (nil otherwise)
local function SkillRank(skill, name)
    if C_TradeSkillUI and C_TradeSkillUI.GetProfessionInfoBySkillLineID then
        local ok, info = pcall(C_TradeSkillUI.GetProfessionInfoBySkillLineID, skill)
        if ok and info and info.maxSkillLevel and info.maxSkillLevel > 0 then
            return info.skillLevel, info.maxSkillLevel
        end
    end
    if GetNumSkillLines and GetSkillLineInfo then
        for i = 1, GetNumSkillLines() do
            local lineName, isHeader, _, rank, _, _, maxRank = GetSkillLineInfo(i)
            if not isHeader and lineName == name then return rank, maxRank end
        end
    end
end

-- the known professions, in the order above: { name, icon, cast (spell name or nil), rank, maxRank }
local function KnownProfessions()
    local list = {}
    for _, p in ipairs(PROFESSIONS) do
        local known
        for i = #p.ranks, 1, -1 do
            if Knows(p.ranks[i]) then known = p.ranks[i] break end
        end
        if known then
            local name, icon = SpellNameIcon(p.ranks[1])
            local cast
            if p.open == "rank" then
                cast = SpellNameIcon(known)
            elseif p.open and Knows(p.open) then
                cast = SpellNameIcon(p.open)
            end
            local rank, maxRank = SkillRank(p.skill, name)
            list[#list + 1] = { name = name or "?", icon = icon, cast = cast, rank = rank, maxRank = maxRank }
        end
    end
    return list
end

local ROW, WIDTH = 30, 190
local panel = CreateFrame("Frame", "EverlakeProfessionsPanel", UIParent)
panel:SetSize(WIDTH, 40)
panel:SetFrameStrata("HIGH")
panel:SetClampedToScreen(true)
panel:Hide()
local bg = panel:CreateTexture(nil, "BACKGROUND")
bg:SetAllPoints()
bg:SetColorTexture(0, 0, 0, 0.8)
local title = panel:CreateFontString(nil, "OVERLAY", "GameFontNormal")
title:SetPoint("TOPLEFT", 8, -6)
title:SetText(PROFESSIONS_TRACKER_HEADER_PROFESSION or TRADE_SKILLS or "Professions")

local buttons = {}
local function Button(i)
    local b = buttons[i]
    if b then return b end
    b = CreateFrame("Button", "EverlakeProfessionsButton" .. i, panel, "SecureActionButtonTemplate")
    b:SetSize(WIDTH - 12, ROW - 2)
    b:SetPoint("TOPLEFT", 6, -22 - (i - 1) * ROW)
    b:RegisterForClicks("AnyUp", "AnyDown")
    b.icon = b:CreateTexture(nil, "ARTWORK")
    b.icon:SetSize(ROW - 4, ROW - 4)
    b.icon:SetPoint("LEFT")
    b.label = b:CreateFontString(nil, "OVERLAY", "GameFontHighlight")
    b.label:SetPoint("LEFT", b.icon, "RIGHT", 6, 0)
    b.label:SetPoint("RIGHT")
    b.label:SetJustifyH("LEFT")
    b:SetHighlightTexture("Interface\\Buttons\\UI-Common-MouseHilight", "ADD")
    buttons[i] = b
    return b
end

local pending
local function Refresh()
    if InCombatLockdown() then pending = true return end
    pending = false
    local list = KnownProfessions()
    for i, p in ipairs(list) do
        local b = Button(i)
        b.icon:SetTexture(p.icon)
        local text = p.name
        if p.rank then text = text .. string.format("  |cffaaaaaa%d/%d|r", p.rank, p.maxRank) end
        b.label:SetText(text)
        if p.cast then
            b:SetAttribute("type", "spell")
            b:SetAttribute("spell", p.cast)
            b.icon:SetDesaturated(false)
            b.label:SetTextColor(1, 1, 1)
        else
            b:SetAttribute("type", nil)
            b.icon:SetDesaturated(true)
            b.label:SetTextColor(0.6, 0.6, 0.6)
        end
        b:Show()
    end
    for i = #list + 1, #buttons do buttons[i]:Hide() end
    panel:SetHeight(28 + math.max(#list, 1) * ROW)
end

-- the spellbook frame of this client: Classic and retail name it differently
local function Spellbook()
    return _G.SpellBookFrame or _G.PlayerSpellsFrame or _G.ProfessionsBookFrame
end

local function Place()
    local book = Spellbook()
    panel:ClearAllPoints()
    if book and book:IsShown() then
        panel:SetPoint("TOPLEFT", book, "TOPRIGHT", 4, -24)
    else
        panel:SetPoint("CENTER", UIParent, "CENTER", 300, 0)
    end
end

local wantShown
local function SetShown(show)
    wantShown = show
    if InCombatLockdown() then pending = true return end
    if show then
        Refresh()
        Place()
        panel:Show()
    else
        panel:Hide()
    end
end

local hooked = {}
local function HookSpellbook()
    local book = Spellbook()
    if not book or hooked[book] then return end
    hooked[book] = true
    book:HookScript("OnShow", function() if EverlakeDB.professionsWithSpellbook ~= false then SetShown(true) end end)
    book:HookScript("OnHide", function() SetShown(false) end)
end

-- Tabs down the side of the profession window, one per known profession, so any of them is one click away from
-- another (the client's own window only knows its two). Attached to whichever window this client opens.
local TAB = 36
local tabs = {}
local windowHooked = {}
local function ProfessionWindows()
    return { _G.ProfessionsFrame, _G.TradeSkillFrame, _G.CraftFrame }
end

local function Tab(window, i)
    tabs[window] = tabs[window] or {}
    local t = tabs[window][i]
    if t then return t end
    t = CreateFrame("Button", nil, window, "SecureActionButtonTemplate")
    t:SetSize(TAB, TAB)
    -- right side, outside the client's own column of profession tabs (about 60 px wide)
    t:SetPoint("TOPLEFT", window, "TOPRIGHT", 66, -40 - (i - 1) * (TAB + 6))
    t:RegisterForClicks("AnyUp", "AnyDown")
    local border = t:CreateTexture(nil, "BACKGROUND")
    border:SetPoint("TOPLEFT", -3, 3)
    border:SetPoint("BOTTOMRIGHT", 3, -3)
    border:SetColorTexture(0, 0, 0, 0.85)
    t.icon = t:CreateTexture(nil, "ARTWORK")
    t.icon:SetAllPoints()
    t:SetHighlightTexture("Interface\\Buttons\\ButtonHilight-Square", "ADD")
    t:SetScript("OnEnter", function(self)
        GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
        GameTooltip:SetText(self.name or "")
        if self.rank then GameTooltip:AddLine(string.format("%d/%d", self.rank, self.maxRank), 1, 1, 1) end
        if not self.cast then GameTooltip:AddLine("No profession window", 0.6, 0.6, 0.6) end
        GameTooltip:Show()
    end)
    t:SetScript("OnLeave", function() GameTooltip:Hide() end)
    tabs[window][i] = t
    return t
end

local function RefreshTabs(window)
    if InCombatLockdown() then pending = true return end
    local list = KnownProfessions()
    for i, p in ipairs(list) do
        local t = Tab(window, i)
        t.icon:SetTexture(p.icon)
        t.name, t.rank, t.maxRank, t.cast = p.name, p.rank, p.maxRank, p.cast
        t:SetAttribute("type", p.cast and "spell" or nil)
        t:SetAttribute("spell", p.cast)
        t.icon:SetDesaturated(not p.cast)
        t:Show()
    end
    for i = #list + 1, #(tabs[window] or {}) do tabs[window][i]:Hide() end
end

local function HookProfessionWindows()
    for _, window in ipairs(ProfessionWindows()) do
        if window and not windowHooked[window] then
            windowHooked[window] = true
            window:HookScript("OnShow", function(self) RefreshTabs(self) end)
            if window:IsShown() then RefreshTabs(window) end
        end
    end
end

local events = CreateFrame("Frame")
for _, e in ipairs({ "PLAYER_LOGIN", "SPELLS_CHANGED", "SKILL_LINES_CHANGED", "LEARNED_SPELL_IN_TAB", "PLAYER_REGEN_ENABLED", "ADDON_LOADED",
        "TRADE_SKILL_SHOW", "CRAFT_SHOW" }) do
    pcall(events.RegisterEvent, events, e)
end
events:SetScript("OnEvent", function(_, event)
    EverlakeDB = EverlakeDB or {}
    HookSpellbook()
    HookProfessionWindows()   -- the profession window's addon loads on demand
    if event == "PLAYER_REGEN_ENABLED" and pending then
        SetShown(wantShown)
    elseif panel:IsShown() then
        Refresh()
    end
    if not InCombatLockdown() then
        for window in pairs(windowHooked) do
            if window:IsShown() then RefreshTabs(window) end
        end
    end
end)

-- /everlake prof: show or hide the panel; it also opens with the spellbook unless turned off with /everlake prof book
function addon.ProfessionsCommand(arg)
    EverlakeDB = EverlakeDB or {}
    if arg == "book" then
        EverlakeDB.professionsWithSpellbook = EverlakeDB.professionsWithSpellbook == false
        print("Everlake: professions panel " .. (EverlakeDB.professionsWithSpellbook and "opens" or "no longer opens") .. " with the spellbook.")
        return
    end
    if arg == "frame" then   -- which profession window this client has, for bug reports
        for _, name in ipairs({ "ProfessionsFrame", "TradeSkillFrame", "CraftFrame", "SpellBookFrame", "PlayerSpellsFrame" }) do
            local f = _G[name]
            print(name .. ": " .. (f and (f:IsShown() and "shown" or "loaded") or "missing") .. (f and windowHooked[f] and ", tabs on" or ""))
        end
        return
    end
    SetShown(not panel:IsShown())
end
