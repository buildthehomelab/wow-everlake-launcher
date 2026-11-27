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
            local cast, castId
            if p.open == "rank" then
                cast, castId = SpellNameIcon(known), known
            elseif p.open and Knows(p.open) then
                cast, castId = SpellNameIcon(p.open), p.open
            end
            local rank, maxRank = SkillRank(p.skill, name)
            list[#list + 1] = { name = name or "?", icon = icon, cast = cast, rank = rank, maxRank = maxRank, skill = p.skill, castId = castId }
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

-- The client's own column of profession tabs on the window's right edge: its buttons, bottom-most last. Our tabs
-- continue that column below them.
local function ClientTabColumn(window)
    local right = window:GetRight()
    if not right then return end
    local column = {}
    for _, child in ipairs({ window:GetChildren() }) do
        local left, width = child:GetLeft(), child:GetWidth()
        if child:IsShown() and left and left >= right - 8 and width and width > 16 and width <= 72 and not child.everlakeTab then
            column[#column + 1] = child
        end
    end
    table.sort(column, function(a, b) return (a:GetBottom() or 0) > (b:GetBottom() or 0) end)
    return column
end

-- What a tab of the client's column stands for: its spell attribute, its icon, or the first line of its tooltip
local function DescribeTab(tab)
    local d = {}
    if tab.GetAttribute then d.spell = tab:GetAttribute("spell") end
    local normal = tab.GetNormalTexture and tab:GetNormalTexture()
    if normal and normal.GetTexture then d.icon = normal:GetTexture() end
    local onEnter = tab:GetScript("OnEnter")
    if onEnter and GameTooltip and not InCombatLockdown() then
        pcall(onEnter, tab)
        local line = _G.GameTooltipTextLeft1
        d.tip = line and line:GetText()
        GameTooltip:Hide()
    end
    return d
end

local function ColumnHas(described, p)
    for _, d in ipairs(described) do
        if (d.spell and (d.spell == p.cast or d.spell == p.name))
            or (d.icon and p.icon and d.icon == p.icon)
            or (d.tip and (d.tip == p.name or (p.cast and d.tip == p.cast))) then
            return true
        end
    end
end

-- /everlake prof frame: what a client tab is made of, to match its look
local function DumpTab(tab)
    local function tex(r)
        local atlas = r.GetAtlas and r:GetAtlas()
        local w, h = r:GetSize()
        local pt, rel, rp, x, y = r:GetPoint(1)
        return string.format("%s %s %dx%d %s>%s %d,%d%s", r:GetDrawLayer() or "?", atlas and ("atlas " .. atlas) or tostring(r:GetTexture()),
            w or 0, h or 0, pt or "-", rp or "-", x or 0, y or 0, r:IsShown() and "" or " hidden")
    end
    print(string.format("tab %s (%s) %dx%d", tab:GetName() or "unnamed", tab:GetObjectType(), tab:GetWidth(), tab:GetHeight()))
    for _, getter in ipairs({ "GetNormalTexture", "GetPushedTexture", "GetCheckedTexture", "GetHighlightTexture" }) do
        local r = tab[getter] and tab[getter](tab)
        if r then print("  " .. getter:sub(4) .. ": " .. tex(r)) end
    end
    for _, r in ipairs({ tab:GetRegions() }) do
        if r:GetObjectType() == "Texture" then print("  region: " .. tex(r)) end
    end
    for _, c in ipairs({ tab:GetChildren() }) do
        print("  child " .. (c:GetName() or c:GetObjectType()))
        for _, r in ipairs({ c:GetRegions() }) do
            if r:GetObjectType() == "Texture" then print("    region: " .. tex(r)) end
        end
    end
end

-- The client's side tabs (ProfessionsFrame, 1.60): a 55x54 frame with atlas common-sidetab behind a 50x50 icon,
-- common-sidetab-selected over the open profession, common-sidetab-hover on mouse-over. Ours are built the same way.
local SIDETAB_W, SIDETAB_H, SIDETAB_ART_H, SIDETAB_ICON = 55, 54, 59, 50

local function Tab(window, i)
    tabs[window] = tabs[window] or {}
    local t = tabs[window][i]
    if t then return t end
    t = CreateFrame("Button", nil, window, "SecureActionButtonTemplate")
    t.everlakeTab = true
    t:SetSize(SIDETAB_W, SIDETAB_H)
    t:RegisterForClicks("AnyUp", "AnyDown")
    t.back = t:CreateTexture(nil, "BACKGROUND")
    t.back:SetPoint("CENTER")
    t.back:SetSize(SIDETAB_W, SIDETAB_ART_H)
    t.icon = t:CreateTexture(nil, "ARTWORK")
    t.icon:SetSize(SIDETAB_ICON, SIDETAB_ICON)
    t.icon:SetPoint("CENTER")
    t.selected = t:CreateTexture(nil, "OVERLAY")
    t.selected:SetPoint("CENTER")
    t.selected:SetSize(SIDETAB_W, SIDETAB_ART_H)
    t.selected:Hide()
    -- SetAtlas answers false for an atlas the client lacks (C_Texture.GetAtlasInfo found nothing on 1.60 for these)
    if t.back:SetAtlas("common-sidetab") ~= false then
        t.selected:SetAtlas("common-sidetab-selected")
        t:SetHighlightAtlas("common-sidetab-hover", "ADD")
        local hl = t:GetHighlightTexture()
        if hl then hl:ClearAllPoints() hl:SetPoint("CENTER") hl:SetSize(SIDETAB_W, SIDETAB_ART_H) end
    else   -- a client without these atlases: a plain dark frame
        t.back:SetColorTexture(0, 0, 0, 0.85)
        t.selected:SetColorTexture(1, 0.82, 0, 0.35)
        t:SetHighlightTexture("Interface\\Buttons\\ButtonHilight-Square", "ADD")
    end
    t:SetScript("OnEnter", function(self)
        GameTooltip:SetOwner(self, "ANCHOR_RIGHT")
        GameTooltip:SetText(self.name or "")
        if self.rank then GameTooltip:AddLine(string.format("%d/%d", self.rank, self.maxRank), 1, 1, 1) end
        GameTooltip:Show()
    end)
    t:SetScript("OnLeave", function() GameTooltip:Hide() end)
    tabs[window][i] = t
    return t
end

-- Our tab's art sits exactly as on the client's tab: the anchors and sizes of its background, icon, selected overlay and
-- highlight copied from a client tab (centring them left the art a few pixels off).
local function MatchTabArt(model, t)
    if t.matched == model or not model.GetRegions then return end
    local function copy(from, to)
        local w, h = from:GetSize()
        if w and w > 0 then to:SetSize(w, h) end
        if from:GetNumPoints() > 0 then
            to:ClearAllPoints()
            for i = 1, from:GetNumPoints() do
                local point, relative, relativePoint, x, y = from:GetPoint(i)
                to:SetPoint(point, (relative == model or not relative) and t or relative, relativePoint, x, y)
            end
        end
    end
    local selectedDone
    for _, r in ipairs({ model:GetRegions() }) do
        if r:GetObjectType() == "Texture" then
            local atlas = r:GetAtlas()
            if atlas == "common-sidetab" then copy(r, t.back)
            elseif atlas == "common-sidetab-selected" and not selectedDone then copy(r, t.selected) selectedDone = true
            elseif atlas == "common-sidetab-hover" then
                local hl = t:GetHighlightTexture()
                if hl then copy(r, hl) end
            elseif not atlas and r:GetDrawLayer() == "ARTWORK" then copy(r, t.icon) end
        end
    end
    t.matched = model
end

-- the client marks one of its own tabs selected even when the open profession is one of ours: its overlays are
-- hidden then, and given back when the client's own profession is open again
local hiddenSelected = {}
local function ClientSelected(column, show)
    for _, tab in ipairs(column) do
        if tab.GetRegions then
            for _, r in ipairs({ tab:GetRegions() }) do
                if r:GetObjectType() == "Texture" and r:GetAtlas() == "common-sidetab-selected" then
                    if not show and r:IsShown() then
                        r:Hide()
                        hiddenSelected[r] = true
                    elseif show and hiddenSelected[r] then
                        r:Show()
                        hiddenSelected[r] = nil
                    end
                end
            end
        end
    end
end

-- the profession the window shows now (its base skill line), when the client can tell
local function OpenSkillLine()
    if C_TradeSkillUI and C_TradeSkillUI.GetBaseProfessionInfo then
        local ok, info = pcall(C_TradeSkillUI.GetBaseProfessionInfo)
        if ok and info then return info.professionID or info.skillLineID, info.professionName end
    end
end

-- One column down the window's right edge: the client's tabs, then ours, from the top of the window. When they don't
-- fit, all of them shrink evenly so the column ends at the window's bottom edge.
local PAD = 8
local layouts = {}   -- per window: the client's tab size, spacing and distance, read before we move its tabs
local function RefreshTabs(window)
    if InCombatLockdown() then pending = true return end
    local column = ClientTabColumn(window) or {}
    layouts[window] = layouts[window] or {}
    local layout = layouts[window]
    local first = column[1]
    if first and not layout.size then
        -- the client's own spacing and distance from the window, read once before anything is moved
        layout.size = first:GetHeight()
        layout.x = (first:GetLeft() or 0) - (window:GetRight() or 0)
        layout.gap = column[2] and math.max(0, (first:GetBottom() or 0) - (column[2]:GetTop() or 0)) or 6
    end
    local size, gap, x = layout.size or SIDETAB_H, layout.gap or 6, layout.x or 2

    local all = {}
    local described = {}
    -- every client tab stays, the "Professions" overview too: it holds the professions' extra spells (Disenchant,
    -- Bait and Tackle, the secondary professions)
    for _, tab in ipairs(column) do
        all[#all + 1] = tab
        described[#described + 1] = DescribeTab(tab)
    end

    local openSkill, openName = OpenSkillLine()
    -- on the overview page the client still names the last profession opened: nothing of ours is selected there
    local book = type(window.BookPage) == "table" and window.BookPage or nil
    if book and book:IsShown() then openSkill, openName = nil, nil end
    local oursOpen = false
    local n = 0
    for _, p in ipairs(KnownProfessions()) do
        if p.cast and not ColumnHas(described, p) then   -- no tab for a profession with no window
            n = n + 1
            local t = Tab(window, n)
            if first then pcall(MatchTabArt, first, t) end
            t.icon:SetTexture(p.icon)
            local selected = openName == p.name or (openSkill ~= nil and openSkill == p.skill)
            t.selected:SetShown(selected)
            oursOpen = oursOpen or selected
            t.name, t.rank, t.maxRank, t.cast = p.name, p.rank, p.maxRank, p.cast
            t:SetAttribute("type", "spell")
            t:SetAttribute("spell", p.cast)
            t.icon:SetDesaturated(false)
            t:Show()
            all[#all + 1] = t
        end
    end
    for i = n + 1, #(tabs[window] or {}) do tabs[window][i]:Hide() end
    pcall(ClientSelected, column, not oursOpen)

    local height = (window:GetHeight() or 600) - 2 * PAD
    local needed = #all * (size + gap) - gap
    local scale = needed > height and height / needed or 1
    for i, tab in ipairs(all) do
        tab:SetScale(scale)
        tab:ClearAllPoints()
        -- offsets are in the tab's own (scaled) units
        tab:SetPoint("TOPLEFT", window, "TOPRIGHT", x / scale, -(PAD + (i - 1) * (size + gap) * scale) / scale)
    end
end

local function HookProfessionWindows()
    for _, window in ipairs(ProfessionWindows()) do
        if window and not windowHooked[window] then
            windowHooked[window] = true
            -- the client lays out its own tabs as the window opens: place ours a frame later too
            -- switching between the overview page and a profession keeps the window open: follow the page too
            local book = type(window.BookPage) == "table" and window.BookPage or nil
            if book then
                local function follow()
                    if window:IsShown() then
                        RefreshTabs(window)
                        if C_Timer and C_Timer.After then C_Timer.After(0, function() if window:IsShown() then RefreshTabs(window) end end) end
                    end
                end
                book:HookScript("OnShow", follow)
                book:HookScript("OnHide", follow)
            end
            window:HookScript("OnShow", function(self)
                RefreshTabs(self)
                if C_Timer and C_Timer.After then C_Timer.After(0, function() if self:IsShown() then RefreshTabs(self) end end) end
            end)
            if window:IsShown() then RefreshTabs(window) end
        end
    end
end

local events = CreateFrame("Frame")
for _, e in ipairs({ "PLAYER_LOGIN", "SPELLS_CHANGED", "SKILL_LINES_CHANGED", "LEARNED_SPELL_IN_TAB", "PLAYER_REGEN_ENABLED", "ADDON_LOADED",
        "TRADE_SKILL_SHOW", "CRAFT_SHOW", "TRADE_SKILL_LIST_UPDATE", "TRADE_SKILL_DATA_SOURCE_CHANGED" }) do
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
    -- the client may lay its own tabs out again as it refreshes: redo ours a frame later too
    local function relayout()
        if InCombatLockdown() then return end
        for window in pairs(windowHooked) do
            if window:IsShown() then RefreshTabs(window) end
        end
    end
    relayout()
    if C_Timer and C_Timer.After then C_Timer.After(0, relayout) end
end)

-- /everlake prof: show or hide the panel; it also opens with the spellbook unless turned off with /everlake prof book
function addon.ProfessionsCommand(arg)
    EverlakeDB = EverlakeDB or {}
    if arg == "book" then
        EverlakeDB.professionsWithSpellbook = EverlakeDB.professionsWithSpellbook == false
        print("Everlake: professions panel " .. (EverlakeDB.professionsWithSpellbook and "opens" or "no longer opens") .. " with the spellbook.")
        return
    end
    if arg == "bar" then   -- the bar parts of the client's Professions page section (Overview.lua)
        if addon.DumpProfessionBar then pcall(addon.DumpProfessionBar) end
        return
    end
    if arg == "page" then   -- the art of the client's Professions page sections (Overview.lua)
        if addon.DumpProfessionPage then pcall(addon.DumpProfessionPage) end
        return
    end
    if arg == "frame" then   -- which profession window this client has, for bug reports
        for _, name in ipairs({ "ProfessionsFrame", "TradeSkillFrame", "CraftFrame", "SpellBookFrame", "PlayerSpellsFrame", "ProfessionsBookFrame", "PrimaryProfession1", "PrimaryProfession2", "SecondaryProfession1" }) do
            local f = _G[name]
            print(name .. ": " .. (f and (f:IsShown() and "shown" or "loaded") or "missing") .. (f and windowHooked[f] and ", tabs on" or ""))
        end
        for _, window in ipairs(ProfessionWindows()) do
            if window and window:IsShown() then
                local column = ClientTabColumn(window)
                if column and column[1] then
                    local ok, err = pcall(DumpTab, column[1])
                    if not ok then print("tab dump failed: " .. tostring(err)) end
                end
            end
        end
        return
    end
    SetShown(not panel:IsShown())
end

-- for Overview.lua (the Everlake Professions window)
addon.Professions = { Known = KnownProfessions, SpellNameIcon = SpellNameIcon, Knows = Knows, SkillRank = SkillRank }
