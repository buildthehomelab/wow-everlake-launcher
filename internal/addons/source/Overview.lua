-- Every primary profession on the client's Professions page.
--
-- In 1.60 that page is ProfessionsFrame.BookPage: its ProfessionsContentFrame has two sections for the primary
-- professions in the client's own slots (PrimaryProfession1/2; one used on Everlake, which keeps the second slot free
-- so every profession can be learned) above Cooking, Fishing and First Aid. A scrolling list over the two sections
-- shows every known primary profession instead, in sections of their size: spells, rank, rank bar, unlearn X. The
-- secondary professions below stay the client's. In combat the page is left as the client made it.
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

-- English names in the client's per-profession atlases (Profession-overview-Card-Blacksmithing, ...)
local ATLAS_NAME = {
    [171] = "Alchemy", [164] = "Blacksmithing", [333] = "Enchanting", [202] = "Engineering", [773] = "Inscription",
    [755] = "Jewelcrafting", [165] = "Leatherworking", [197] = "Tailoring", [186] = "Mining", [182] = "Herbalism", [393] = "Skinning",
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

-- Every profession in the character's skill list, by skill line: GetProfessionInfo takes any skill list index, not
-- only the ones GetProfessions hands out, so it gives rank, max rank and spellbook spells for every profession.
local function SkillListProfessions()
    local bySkill = {}
    if not GetProfessionInfo then return bySkill end
    for index = 1, 128 do
        local ok, name, icon, rank, maxRank, numSpells, offset, skillLine = pcall(GetProfessionInfo, index)
        if ok and name and skillLine and not bySkill[skillLine] then
            bySkill[skillLine] = { index = index, name = name, icon = icon, rank = rank, maxRank = maxRank,
                numSpells = numSpells, offset = offset, skill = skillLine }
        end
    end
    return bySkill
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
    b.icon = b:CreateTexture(nil, "BORDER")
    b.icon:SetAllPoints()
    -- the client's profession spell button: a shadow round the icon and the profession slot frame over it
    b.shadow = b:CreateTexture(nil, "ARTWORK")
    b.shadow:SetSize(48, 48)
    b.shadow:SetPoint("TOPLEFT", -4, 4)
    b.slot = b:CreateTexture(nil, "OVERLAY")
    b.slot:SetSize(40, 40)
    b.slot:SetPoint("TOPLEFT")
    if b.slot:SetAtlas("Professions-Slot-Frame") ~= false then
        b.back:Hide()
        if b.shadow:SetAtlas("UI-HUD-ActionBar-IconFrame-FlyoutBorderShadow") == false then b.shadow:Hide() end
    else
        b.slot:Hide()
        b.shadow:Hide()
        b.icon:SetTexCoord(0.07, 0.93, 0.07, 0.93)
    end
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
    bar:SetSize(440, 17)   -- the client's profession bar
    bar:SetStatusBarTexture("Interface\\TargetingFrame\\UI-StatusBar")
    bar.bg = bar:CreateTexture(nil, "BACKGROUND")
    bar.bg:SetPoint("TOPLEFT")
    bar.bg:SetSize(440, 22)
    if bar.bg:SetAtlas("Profession-ProgressBar-BG") == false then
        bar.bg:ClearAllPoints()
        bar.bg:SetPoint("TOPLEFT", -1, 1)
        bar.bg:SetPoint("BOTTOMRIGHT", 1, -1)
        bar.bg:SetColorTexture(0, 0, 0, 0.9)
    end
    bar.text = bar:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    bar.text:SetPoint("CENTER")
    return bar
end

local function SetBar(bar, skill, name, rank, maxRank, fillAtlas)
    if rank and maxRank and maxRank > 0 then
        local fill = bar:GetStatusBarTexture()
        if fillAtlas and fill and fill:SetAtlas(fillAtlas) ~= false then
            bar:SetStatusBarColor(1, 1, 1)
        else
            bar:SetStatusBarTexture("Interface\\TargetingFrame\\UI-StatusBar")
            local c = BAR_COLORS[skill] or { 0.9, 0.6, 0.1 }
            bar:SetStatusBarColor(c[1], c[2], c[3])
        end
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

-- Art the client's own (hidden) first section carries, read at run time: its profession card names the profession it
-- shows (Profession-overview-Card-<name>); its bar fill and red X atlases are reused, the fill with the name swapped.
local art
local function ClientArt(p1)
    if art then return art end
    local found = { cross = {}, atlases = {} }
    local function walk(frame, depth)
        if depth > 4 then return end
        if frame.GetStatusBarTexture then
            local t = frame:GetStatusBarTexture()
            local a = t and t.GetAtlas and t:GetAtlas()
            if a then found.barAtlas = found.barAtlas or a end
        end
        for _, r in ipairs({ frame:GetRegions() }) do
            local a = r.GetObjectType and r:GetObjectType() == "Texture" and r:GetAtlas()
            if a then
                found.atlases[#found.atlases + 1] = a
                local card = a:match("^Profession%-overview%-Card%-(.+)$")
                if card then found.cardName = card end
                if a:find("crossmark", 1, true) then found.cross[#found.cross + 1] = a end
            end
        end
        for _, c in ipairs({ frame:GetChildren() }) do walk(c, depth + 1) end
    end
    walk(p1, 0)
    if not found.cardName then return nil end   -- the client has not filled its section yet: try again later
    -- the bar fill: the status bar texture when it names the profession, else any other atlas naming it, one with
    -- "Bar" or "Fill" in its name first
    local name = found.cardName
    if found.barAtlas and found.barAtlas:find(name, 1, true) then found.fill = found.barAtlas end
    if not found.fill then
        for _, a in ipairs(found.atlases) do
            if a:find(name, 1, true) and not a:find("overview%-Card") then
                if a:find("Bar") or a:find("Fill") then found.fill = a break end
                found.fill = found.fill or a
            end
        end
    end
    for _, a in ipairs(found.cross) do
        if a:find("%-pressed$") then found.crossPushed = a
        elseif a:find("%-highlight$") or a:find("%-hover$") then found.crossHighlight = a
        elseif not a:find("%-disabled$") then found.crossNormal = found.crossNormal or a end
    end
    if not found.crossNormal and found.crossPushed then found.crossNormal = found.crossPushed:gsub("%-pressed$", "") end
    art = found
    return art
end

local function FillAtlas(skill)
    local name = ATLAS_NAME[skill]
    if not (art and art.fill and art.cardName and name) then return nil end
    local swapped, n = art.fill:gsub(art.cardName, name)
    return n > 0 and swapped or nil
end

-- An exact copy of the client's profession bar (the StatusBar frame of its hidden first section): every texture and
-- child frame with its layer, size and anchors, the inner status bar as a real status bar, the profession's name
-- swapped in the atlases that carry it. A status bar lists its own fill among its regions; that one is not copied
-- (it would be a second, full width fill). Font strings are not copied: the copy has its own rank text.
local function BarModel(p1)
    if type(p1.StatusBar) == "table" and p1.StatusBar.GetRegions then return p1.StatusBar end
    for _, c in ipairs({ p1:GetChildren() }) do
        if c.IsObjectType and c:IsObjectType("StatusBar") then return c end
    end
end

local function CloneBar(model, parent, p1)
    local map, pairsList, bars, swaps = { [p1] = parent }, {}, {}, {}
    local isBar = function(frame) return frame.IsObjectType and frame:IsObjectType("StatusBar") end
    local root = CreateFrame(isBar(model) and "StatusBar" or "Frame", nil, parent)
    map[model] = root
    local function build(from, to, depth)
        pairsList[#pairsList + 1] = { from, to }
        local fill = isBar(from) and from:GetStatusBarTexture() or nil
        if fill then
            local atlas = fill:GetAtlas()
            to:SetStatusBarTexture(atlas and "Interface\\Buttons\\WHITE8x8" or (fill:GetTexture() or "Interface\\TargetingFrame\\UI-StatusBar"))
            if atlas then to:GetStatusBarTexture():SetAtlas(atlas) end
            to:SetStatusBarColor(fill:GetVertexColor())
            bars[#bars + 1] = { bar = to, atlas = atlas }
        end
        for _, r in ipairs({ from:GetRegions() }) do
            if r ~= fill and r:GetObjectType() == "Texture" then
                local c = to:CreateTexture(nil, r:GetDrawLayer())
                map[r] = c
                pairsList[#pairsList + 1] = { r, c }
            end
        end
        if depth < 3 then
            for _, child in ipairs({ from:GetChildren() }) do
                local c = CreateFrame(isBar(child) and "StatusBar" or "Frame", nil, to)
                c:SetFrameLevel(to:GetFrameLevel() + math.max(0, child:GetFrameLevel() - from:GetFrameLevel()))
                map[child] = c
                build(child, c, depth + 1)
            end
        end
    end
    build(model, root, 0)
    for _, pair in ipairs(pairsList) do
        local from, to = pair[1], pair[2]
        local w, h = from:GetSize()
        if w and w > 0 then to:SetSize(w, h) end
        if from:GetNumPoints() > 0 then
            to:ClearAllPoints()
            for i = 1, from:GetNumPoints() do
                local point, relative, relativePoint, x, y = from:GetPoint(i)
                to:SetPoint(point, map[relative] or to:GetParent(), relativePoint, x, y)
            end
        elseif to ~= root then
            to:SetAllPoints()
        end
        if from:GetObjectType() == "Texture" then
            local atlas = from:GetAtlas()
            if atlas then
                to:SetAtlas(atlas)
                swaps[#swaps + 1] = { tex = to, atlas = atlas }
            elseif from:GetTexture() then
                to:SetTexture(from:GetTexture())
                to:SetTexCoord(from:GetTexCoord())
            end
            to:SetBlendMode(from:GetBlendMode())
            to:SetVertexColor(from:GetVertexColor())
            to:SetShown(from:IsShown())
        end
    end
    if #bars == 0 then root:Hide() return nil end
    local textHolder = CreateFrame("Frame", nil, root)
    textHolder:SetAllPoints(bars[1].bar)
    textHolder:SetFrameLevel(root:GetFrameLevel() + 10)
    root.text = textHolder:CreateFontString(nil, "OVERLAY", "GameFontHighlightSmall")
    root.text:SetPoint("CENTER")
    root.bars, root.swaps, root.cloned = bars, swaps, true
    return root
end

-- one profession on a cloned bar: values, and its own name in the atlases that named the client's profession
local function SetClonedBar(root, skill, name, rank, maxRank)
    local mine = ATLAS_NAME[skill]
    local function swapped(atlas)
        if not (atlas and art and art.cardName and mine) then return atlas end
        local s2 = atlas:gsub(art.cardName, mine)
        return s2
    end
    for _, sw in ipairs(root.swaps) do
        if sw.tex:SetAtlas(swapped(sw.atlas)) == false then sw.tex:SetAtlas(sw.atlas) end
    end
    if rank and maxRank and maxRank > 0 then
        for _, b in ipairs(root.bars) do
            b.bar:SetMinMaxValues(0, maxRank)
            b.bar:SetValue(rank)
            if b.atlas then
                local fill = b.bar:GetStatusBarTexture()
                if fill and fill:SetAtlas(swapped(b.atlas)) == false then fill:SetAtlas(b.atlas) end
            end
        end
        root.text:SetText(string.format("%s %d/%d", name or "", rank, maxRank))
        root:Show()
    else
        root:Hide()
    end
end

local list, content, page
local sections = {}
local SECTION_GAP, SPELL_STEP = 6, 46
local sectionW, sectionH = 663, 142

local function PrimarySection(i)
    local f = sections[i]
    if not f then
        f = CreateFrame("Frame", nil, content)
        f.card = f:CreateTexture(nil, "BACKGROUND")
        f.card:SetAllPoints()
        f.plain = Panel(f)   -- when the client has no card for this profession
        f.plain:SetAllPoints()
        f.plain:SetFrameLevel(f:GetFrameLevel())
        f.header = f:CreateFontString(nil, "OVERLAY", "GameFontNormal")
        f.header:SetPoint("TOPLEFT", 18, -26)
        f.spells = {}
        f.bar = Bar(f)
        f.unlearn = UnlearnButton(f)
        f.unlearn:SetPoint("LEFT", f.bar, "RIGHT", 6, 0)
        sections[i] = f
    end
    f:SetWidth(sectionW)
    if f.bar.cloned ~= true then   -- a cloned bar keeps the client's own place in the section
        f.bar:ClearAllPoints()
        f.bar:SetPoint("TOPLEFT", f, "TOPLEFT", math.min(math.floor(sectionW * 0.32), sectionW - 440 - 44), -66)
    end
    return f
end

local function FillPrimary(f, entry, spells, p1)
    f.header:SetText(entry.name)
    for k = 1, math.max(#spells, #f.spells) do
        local b = f.spells[k]
        if spells[k] then
            if not b then
                b = SpellButton(f)
                b:SetPoint("TOPLEFT", 14, -54 - (k - 1) * SPELL_STEP)
                f.spells[k] = b
            end
            SetSpell(b, spells[k], k == 1 and RankTitle(entry.maxRank) or nil)
        elseif b then
            b:Hide()
        end
    end
    if not spells[1] then   -- nothing to cast (Skinning without Tanning): the profession's icon and name
        local b = f.spells[1]
        if not b then
            b = SpellButton(f)
            b:SetPoint("TOPLEFT", 14, -54)
            f.spells[1] = b
        end
        b.spellId = nil
        b:SetAttribute("type", nil)
        b.icon:SetTexture(entry.icon)
        b.label:SetText(entry.name)
        b.sub:SetText(RankTitle(entry.maxRank))
        b:Show()
    end
    local card = ATLAS_NAME[entry.skill] and ("Profession-overview-Card-" .. ATLAS_NAME[entry.skill])
    local hasCard = card and f.card:SetAtlas(card) ~= false
    f.card:SetShown(hasCard and true or false)
    f.plain:SetShown(not hasCard)
    local model = art and p1 and BarModel(p1)
    if model and not f.cloneTried then
        f.cloneTried = true
        local ok, clone = pcall(CloneBar, model, f, p1)
        if ok and clone then
            f.plainBar = f.bar
            f.plainBar:Hide()
            f.bar = clone
            f.unlearn:ClearAllPoints()
            f.unlearn:SetPoint("LEFT", clone, "RIGHT", 6, 0)
        end
    end
    if f.bar.cloned == true then
        SetClonedBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank)
    else
        SetBar(f.bar, entry.skill, entry.name, entry.rank, entry.maxRank, FillAtlas(entry.skill))
    end
    if art and art.crossNormal and not f.unlearn.clientArt then   -- the client's red X
        f.unlearn.clientArt = true
        f.unlearn:SetSize(21, 21)
        f.unlearn:SetNormalAtlas(art.crossNormal)
        if art.crossPushed then f.unlearn:SetPushedAtlas(art.crossPushed) end
        if art.crossHighlight then f.unlearn:SetHighlightAtlas(art.crossHighlight, "ADD") end
    end
    f.unlearn.skill, f.unlearn.profession = entry.skill, entry.name
    f.unlearn:SetShown(f.bar:IsShown())
    local height = math.max(sectionH, 54 + math.max(1, #spells) * SPELL_STEP + 4)
    f:SetHeight(height)
    f:Show()
    return height
end

-- the client's page and its two primary sections, when this client has them
local function PageParts()
    local frame = _G.ProfessionsFrame
    local book = frame and type(frame.BookPage) == "table" and frame.BookPage or nil
    local holder = book and type(book.ProfessionsContentFrame) == "table" and book.ProfessionsContentFrame or nil
    local p1 = (holder and type(holder.PrimaryProfession1) == "table" and holder.PrimaryProfession1) or _G.PrimaryProfession1
    local p2 = (holder and type(holder.PrimaryProfession2) == "table" and holder.PrimaryProfession2) or _G.PrimaryProfession2
    return book, holder, p1, p2
end

local pending
local function Refresh()
    if InCombatLockdown() then pending = true return end
    pending = false
    local book, holder, p1, p2 = PageParts()
    if not (book and holder and p1 and p2) or not book:IsVisible() then return end
    if not list then
        list = TryCreate("ScrollFrame", "EverlakeProfessionsList", holder, "UIPanelScrollFrameTemplate")
        list:SetPoint("TOPLEFT", p1, "TOPLEFT", 0, 0)
        list:SetPoint("BOTTOMRIGHT", p2, "BOTTOMRIGHT", -24, 0)   -- room for the scroll bar
        list:EnableMouseWheel(true)
        list:SetScript("OnMouseWheel", function(self, delta)
            local range = self:GetVerticalScrollRange() or 0
            self:SetVerticalScroll(math.max(0, math.min(range, (self:GetVerticalScroll() or 0) - delta * 60)))
        end)
        content = CreateFrame("Frame", nil, list)
        list:SetScrollChild(content)
        -- the client shows its two sections again as it refreshes the page: keep them covered
        for _, covered in ipairs({ p1, p2 }) do
            covered:HookScript("OnShow", function(self)
                if list and list:IsShown() and not InCombatLockdown() then self:Hide() end
            end)
        end
    end
    ClientArt(p1)
    local w, h = p1:GetSize()
    if w and w > 100 then sectionW = math.floor(w - 24) end
    if h and h > 60 then sectionH = math.floor(h) end

    local skillList = SkillListProfessions()
    local y, n = 0, 0
    for _, entry in ipairs(P.Known()) do
        n = n + 1
        local client = skillList[entry.skill]
        if client then   -- rank from the skill list when the addon's own lookup had none
            entry.rank = entry.rank or client.rank
            entry.maxRank = (entry.maxRank and entry.maxRank > 0) and entry.maxRank or client.maxRank
        end
        local spells = (client and (client.numSpells or 0) > 0) and BookSpells(client.index) or {}
        if #spells == 0 then spells = StaticSpells(entry) end
        local f = PrimarySection(n)
        f:ClearAllPoints()
        f:SetPoint("TOPLEFT", 0, -y)
        y = y + FillPrimary(f, entry, spells, p1) + SECTION_GAP
    end
    for i = n + 1, #sections do sections[i]:Hide() end
    content:SetSize(sectionW, math.max(1, y))
    p1:Hide()
    p2:Hide()
    list:Show()
end

local hooked = {}
local function Hook()
    local book = PageParts()
    if book and not hooked[book] then
        hooked[book] = true
        book:HookScript("OnShow", function()
            Refresh()
            if C_Timer and C_Timer.After then C_Timer.After(0, Refresh) end   -- once the client has filled the page
        end)
    end
    local frame = _G.ProfessionsFrame
    if frame and not hooked[frame] then
        hooked[frame] = true
        frame:HookScript("OnShow", function()
            if C_Timer and C_Timer.After then C_Timer.After(0, Refresh) else Refresh() end
        end)
    end
end

-- /everlake prof page: the art of the client's first primary section (it is hidden under the list, but kept), to match
-- its look: every texture with atlas or file, layer and size, through its child frames and status bar.
local function DumpPage()
    local _, _, p1 = PageParts()
    if not p1 then print("No PrimaryProfession1 in this client.") return end
    local function tex(r)
        local atlas = r.GetAtlas and r:GetAtlas()
        local w, h = r:GetSize()
        local point, rel, relPoint, x, y = r:GetPoint(1)
        return string.format("%s %s %dx%d @%s %d,%d", r:GetDrawLayer() or "?", atlas and ("atlas " .. atlas) or tostring(r:GetTexture()),
            w or 0, h or 0, point or "-", x or 0, y or 0)
    end
    local function key(parent, child)
        for k, v in pairs(parent) do if v == child and type(k) == "string" then return k end end
        return child:GetName() or child:GetObjectType()
    end
    local function walk(frame, depth, label)
        if depth > 3 then return end
        local w, h = frame:GetSize()
        print(string.rep(" ", depth) .. label .. string.format(" (%s %dx%d)", frame:GetObjectType(), w or 0, h or 0))
        if frame.GetStatusBarTexture and frame:GetStatusBarTexture() then
            print(string.rep(" ", depth + 1) .. "bar: " .. tex(frame:GetStatusBarTexture()))
        end
        for _, r in ipairs({ frame:GetRegions() }) do
            if r:GetObjectType() == "Texture" and (r:GetAtlas() or r:GetTexture()) then print(string.rep(" ", depth + 1) .. tex(r)) end
        end
        for _, c in ipairs({ frame:GetChildren() }) do walk(c, depth + 1, key(frame, c)) end
    end
    walk(p1, 0, "PrimaryProfession1")
end
addon.DumpProfessionPage = DumpPage

-- /everlake prof bar: only the bar parts of the client's first section, and what was picked from them
local function DumpBar()
    local _, _, p1 = PageParts()
    if not p1 then print("No PrimaryProfession1 in this client.") return end
    local function walk(frame, depth, label)
        if depth > 5 then return end
        local isBar = label:find("Bar") or (frame.GetStatusBarTexture and true)
        if isBar then
            local w, h = frame:GetSize()
            print(string.rep(" ", depth) .. label .. string.format(" (%s %dx%d)", frame:GetObjectType(), w or 0, h or 0))
            if frame.GetStatusBarTexture and frame:GetStatusBarTexture() then
                local t = frame:GetStatusBarTexture()
                print(string.rep(" ", depth + 1) .. "fill: " .. tostring(t:GetAtlas() or t:GetTexture()))
            end
            for _, r in ipairs({ frame:GetRegions() }) do
                if r:GetObjectType() == "Texture" then
                    print(string.rep(" ", depth + 1) .. (r:GetDrawLayer() or "?") .. " " .. tostring(r:GetAtlas() or r:GetTexture()))
                end
            end
        end
        for _, c in ipairs({ frame:GetChildren() }) do
            local key
            for k, v in pairs(frame) do if v == c and type(k) == "string" then key = k break end end
            walk(c, depth + 1, key or c:GetName() or c:GetObjectType())
        end
    end
    walk(p1, 0, "PrimaryProfession1")
    print("picked: card " .. tostring(art and art.cardName) .. ", fill " .. tostring(art and art.fill))
end
addon.DumpProfessionBar = DumpBar

local events = CreateFrame("Frame")
for _, e in ipairs({ "PLAYER_LOGIN", "ADDON_LOADED", "SKILL_LINES_CHANGED", "SPELLS_CHANGED", "LEARNED_SPELL_IN_TAB", "PLAYER_REGEN_ENABLED",
        "TRADE_SKILL_SHOW" }) do
    pcall(events.RegisterEvent, events, e)
end
events:SetScript("OnEvent", function(_, event)
    Hook()
    if event ~= "PLAYER_REGEN_ENABLED" or pending then Refresh() end
end)
